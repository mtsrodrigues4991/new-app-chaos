package experiment

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/verdict"
)

// RunData é o que se sabe de uma execução terminada.
type RunData struct {
	Run      store.Run
	Windows  []load.Window
	Events   []store.FaultEvent
	Summary  *load.Summary
	Versions []store.Version
}

// Measure avalia uma execução: se ela vale para a comparação e o que se mediu nela.
func Measure(arm string, m Metric, d RunData) ArmRun {
	r := ArmRun{Arm: arm, RunID: d.Run.ID, Status: string(d.Run.Status), Values: map[string]float64{}}
	sc := d.Run.Scenario
	for _, v := range d.Versions {
		if v.Role != store.RoleTarget {
			continue
		}
		r.Commit = v.Commit
		var ids []string
		for _, im := range v.Images {
			id := im.ID
			if id == "" {
				id = im.Image
			}
			ids = append(ids, id)
		}
		sort.Strings(ids)
		r.Version = strings.Join(ids, " + ")
		if v.Mixed {
			r.Invalid = "os pods do alvo rodavam versões diferentes"
		}
	}
	invalid := func(why string) ArmRun {
		if r.Invalid == "" {
			r.Invalid = why
		}
		return r
	}
	if d.Run.Status != store.RunCompleted {
		why := "a execução não terminou (" + string(d.Run.Status) + ")"
		if d.Run.Error != "" {
			why += ": " + d.Run.Error
		}
		return invalid(why)
	}
	// Uma falha que não entrou ou não saiu muda o que foi testado.
	for _, ev := range d.Events {
		if ev.State != store.FaultReverted {
			return invalid(fmt.Sprintf("a falha %s não foi aplicada e revertida (%s)", ev.Type, ev.State))
		}
	}
	if sc.HasChaos() && len(d.Events) < len(sc.Faults) {
		return invalid("nem todas as falhas do cenário rodaram")
	}
	if sc.HasChaos() {
		r.Values[MetricScore] = float64(verdict.Evaluate(sc, d.Windows, d.Events).Score)
	}
	if sc.HasLoad() {
		s := d.Summary
		switch {
		case s == nil:
			return invalid("a execução não tem o resumo do k6")
		case s.DroppedIterations > 0:
			return invalid(fmt.Sprintf("o gerador não entregou a carga pedida (%d requisições descartadas)", s.DroppedIterations))
		case s.Steady.Requests == 0:
			return invalid("nenhuma requisição na janela estável")
		}
		seg := s.Steady
		if e := m.Endpoint; e != nil {
			if *e >= len(s.Endpoints) {
				return invalid("o endpoint escolhido não tem números próprios nesta execução")
			}
			seg = s.Endpoints[*e].Segment
			if seg.Requests == 0 {
				return invalid("o endpoint escolhido não recebeu requisições na janela estável")
			}
		}
		r.Values[MetricP50], r.Values[MetricP95], r.Values[MetricP99], r.Values[MetricAvg] = seg.P50Ms, seg.P95Ms, seg.P99Ms, seg.AvgMs
		r.Values[MetricErrorRate] = seg.ErrorRatePct
		if cpu, ok := steadyCPU(d.Windows, sc.Profile); ok && s.Steady.RPS > 0 {
			r.Values[MetricCPUPerRequest] = cpu / s.Steady.RPS
		}
	}
	if _, ok := r.Values[m.Name]; !ok {
		return invalid("a métrica do experimento não foi medida nesta execução")
	}
	if r.Invalid != "" {
		return r
	}
	r.Valid = true
	return r
}

// steadyCPU é a CPU média do alvo depois do aquecimento (false se não houve leitura).
func steadyCPU(ws []load.Window, p scenario.Profile) (float64, bool) {
	var sum float64
	n := 0
	for _, w := range ws {
		if w.T >= int64(p.WarmupSeconds) && w.RPS > 0 {
			sum += w.CPU
			n++
		}
	}
	if n == 0 || sum == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

func values(runs []ArmRun, arm, metric string) []float64 {
	var out []float64
	for _, r := range runs {
		if r.Arm == arm && r.Valid {
			if v, ok := r.Values[metric]; ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// guardsFor são as métricas de guarda de uma métrica principal: uma correção que baixa o p95
// e dobra os erros não é ganho.
func guardsFor(primary string) []string {
	var out []string
	for _, g := range []string{MetricErrorRate, MetricScore, MetricCPUPerRequest} {
		if g != primary {
			out = append(out, g)
		}
	}
	return out
}

// NoisePct é o piso de ruído que um teste A/A mediu: a maior distância de zero do intervalo
// de 95%, em % da mediana. Diferenças desse tamanho aparecem sem nenhuma mudança na aplicação.
func NoisePct(c stats.Comparison) float64 {
	return math.Max(math.Abs(c.LoPct), math.Abs(c.HiPct))
}

// Decide aplica a regra de decisão às execuções válidas. aaNoise é o piso de ruído do teste
// A/A mais recente do mesmo cenário e métrica (nil = nunca medido); não é usado num teste A/A.
func Decide(e Experiment, aaNoise *float64) Result {
	m := e.Metric.Name
	res := Result{Guards: []Guard{}, Notes: []string{}}
	res.Primary = stats.Compare(values(e.Runs, ArmA, m), values(e.Runs, ArmB, m), Direction(m), MarginFor(m, e.MarginPct))
	res.Outcome = res.Primary.Outcome
	res.Notes = append(res.Notes, "Métrica principal: "+res.Primary.Why+".")

	for _, g := range guardsFor(m) {
		a, b := values(e.Runs, ArmA, g), values(e.Runs, ArmB, g)
		if len(a) == 0 || len(b) == 0 {
			continue
		}
		c := stats.Compare(a, b, Direction(g), MarginFor(g, e.MarginPct))
		res.Guards = append(res.Guards, Guard{Metric: g, Comparison: c})
		if c.Outcome == stats.Worsened && res.Outcome != stats.Inconclusive {
			if res.Primary.Outcome == stats.Worsened {
				res.Notes = append(res.Notes, fmt.Sprintf("A métrica de guarda %s também piorou.", g))
			} else {
				res.Notes = append(res.Notes, fmt.Sprintf("A métrica de guarda %s piorou: o resultado é \"piorou\" mesmo com a principal em \"%s\".", g, res.Primary.Outcome))
			}
			res.Outcome = stats.Worsened
		}
	}

	// Versões: cada lado tem que ter rodado uma versão só, e (num A/B) os lados têm que diferir.
	va, vb := versionsOf(e.Runs, ArmA), versionsOf(e.Runs, ArmB)
	changed := false
	for arm, vs := range map[string][]string{ArmA: va, ArmB: vb} {
		if len(vs) > 1 {
			changed = true
			res.Notes = append(res.Notes, fmt.Sprintf("A versão de %s mudou durante o experimento (%d imagens diferentes): as execuções não medem a mesma coisa.", arm, len(vs)))
		}
	}
	if changed {
		res.Outcome = stats.Inconclusive
	}
	if !e.AA() && len(va) == 1 && len(vb) == 1 && va[0] == vb[0] {
		res.Notes = append(res.Notes, "A e B rodam a mesma imagem: a diferença entre eles, se houver, vem só da configuração (variáveis de ambiente, réplicas, limites), não do código.")
	}

	if e.AA() {
		n := NoisePct(res.Primary)
		if res.Primary.NA >= stats.MinRuns && res.Primary.NB >= stats.MinRuns {
			res.NoisePct = &n
			res.Notes = append(res.Notes, fmt.Sprintf("Piso de ruído deste cenário neste ambiente: %.1f%% na métrica principal. Ganhos menores que cerca de %.1f%% não são detectáveis aqui.", n, 2*n))
		}
		if res.Outcome == stats.Improved || res.Outcome == stats.Worsened {
			res.Notes = append(res.Notes, "A mesma versão dos dois lados deu diferença: o ambiente não está estável o bastante para comparar. Repita o teste A/A antes de qualquer A/B.")
		}
		return res
	}
	if aaNoise == nil {
		res.Notes = append(res.Notes, "Não há teste A/A deste cenário com esta métrica: o piso de ruído do ambiente é desconhecido. Rode um antes de confiar num ganho pequeno.")
		return res
	}
	res.NoisePct = aaNoise
	if res.Outcome == stats.Improved && math.Abs(res.Primary.DiffPct) < 2**aaNoise {
		res.Outcome = stats.Inconclusive
		res.Notes = append(res.Notes, fmt.Sprintf("O ganho medido (%.1f%%) é menor que duas vezes o piso de ruído do último teste A/A (%.1f%%): não dá para separá-lo do ruído.", math.Abs(res.Primary.DiffPct), *aaNoise))
	}
	return res
}

func versionsOf(runs []ArmRun, arm string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range runs {
		if r.Arm == arm && r.Valid && r.Version != "" && !seen[r.Version] {
			seen[r.Version] = true
			out = append(out, r.Version)
		}
	}
	return out
}
