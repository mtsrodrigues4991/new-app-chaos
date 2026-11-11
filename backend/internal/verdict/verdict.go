// Package verdict calcula, sem IA, o veredito de uma execução: cada segundo fica dentro ou
// fora dos SLOs do cenário, e a nota de resiliência (0 a 100) sai do tempo dentro dos SLOs
// durante cada falha e do tempo de recuperação depois dela, no estilo do Resiliency Score do krkn.
//
// O cálculo é determinístico: a mesma execução sempre dá a mesma nota. A IA só explica.
package verdict

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Pesos e limites da nota. Ficam aqui (e no README) para a nota ser explicável.
const (
	// Uma falha vale 70% pelo tempo dentro dos SLOs enquanto ativa e 30% pela recuperação.
	WeightDuring   = 0.7
	WeightRecovery = 0.3
	// Com falhas, o tempo fora delas (linha de base) pesa 20% e as falhas 80%.
	WeightBaseline = 0.2
	// Recuperar em até RecoveryFast segundos vale nota cheia; a nota cai até zero em RecoverySlow.
	RecoveryFast = 5
	RecoverySlow = 60
	// Recuperado = RecoveryStreak segundos seguidos dentro dos SLOs.
	RecoveryStreak = 3
)

// Grade resume a nota em palavras.
type Grade string

const (
	GradeResilient Grade = "resiliente" // 90 ou mais
	GradeWarning   Grade = "atencao"    // 70 a 89
	GradeFragile   Grade = "fragil"     // abaixo de 70
	GradeNoData    Grade = "sem_dados"
)

// Phase é um trecho da execução avaliado contra os SLOs.
type Phase struct {
	Seconds       int     `json:"seconds"`       // segundos avaliados
	InSLO         int     `json:"inSlo"`         // segundos dentro dos SLOs
	CompliancePct float64 `json:"compliancePct"` // InSLO / Seconds
}

// FaultResult é o efeito de uma falha da linha do tempo.
type FaultResult struct {
	FaultID   string             `json:"faultId"`
	Type      scenario.FaultType `json:"type"`
	Params    map[string]string  `json:"params,omitempty"`
	State     store.FaultState   `json:"state"`
	Start     float64            `json:"start"` // instante real em que foi aplicada
	End       float64            `json:"end"`   // instante real em que foi revertida
	During    Phase              `json:"during"`
	Recovered bool               `json:"recovered"`
	// RecoverySeconds: do fim da falha até RecoveryStreak segundos seguidos dentro dos SLOs.
	RecoverySeconds *int `json:"recoverySeconds,omitempty"`
	// Pior momento durante a falha e na recuperação.
	WorstP95Ms        *float64 `json:"worstP95Ms,omitempty"`
	WorstErrorRatePct *float64 `json:"worstErrorRatePct,omitempty"`
	StalledSeconds    int      `json:"stalledSeconds,omitempty"`
	ProbeFailures     int      `json:"probeFailures,omitempty"`
	Score             int      `json:"score"`
}

// Check é um SLO conferido na execução inteira.
type Check struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Actual string `json:"actual"`
	Pass   bool   `json:"pass"`
}

// Verdict é o resultado da avaliação de uma execução.
type Verdict struct {
	Score    int           `json:"score"`
	Grade    Grade         `json:"grade"`
	SLO      scenario.SLO  `json:"slo"`
	Overall  Phase         `json:"overall"`
	Baseline Phase         `json:"baseline"` // fora das falhas e das recuperações
	Faults   []FaultResult `json:"faults"`
	Checks   []Check       `json:"checks"`
	Notes    []string      `json:"notes,omitempty"`
}

// second é a avaliação de um segundo: evaluated=false quando não há o que medir (sem carga e sem sonda).
type second struct {
	evaluated, ok bool
	w             load.Window
}

func classify(sc scenario.Scenario, slo scenario.SLO, w load.Window) second {
	s := second{w: w}
	if sc.HasLoad() {
		if w.Stalled {
			return second{evaluated: true, ok: false, w: w}
		}
		if w.RPS == 0 {
			return s // sem requisições e sem VUs: nada a avaliar
		}
		s.evaluated = true
		// Sem latência (nenhuma resposta no segundo) já conta como fora.
		s.ok = w.P95 != nil && *w.P95 <= slo.P95Ms && load.Val(w.ErrorRate)*100 <= slo.ErrorRatePct
		return s
	}
	if w.Probe != nil {
		s.evaluated = true
		s.ok = w.Probe.OK && w.Probe.Ms <= slo.P95Ms
	}
	return s
}

func (p *Phase) add(s second) {
	if !s.evaluated {
		return
	}
	p.Seconds++
	if s.ok {
		p.InSLO++
	}
}

func (p *Phase) finish() {
	if p.Seconds > 0 {
		p.CompliancePct = round1(float64(p.InSLO) / float64(p.Seconds) * 100)
	}
}

func (p Phase) ratio() float64 {
	if p.Seconds == 0 {
		return 1
	}
	return float64(p.InSLO) / float64(p.Seconds)
}

// Evaluate calcula o veredito. events são os registros reais das falhas (store.FaultEvents).
func Evaluate(sc scenario.Scenario, windows []load.Window, events []store.FaultEvent) Verdict {
	slo := sc.EffectiveSLO()
	v := Verdict{SLO: slo, Faults: []FaultResult{}, Checks: []Check{}}
	if len(windows) == 0 {
		v.Grade = GradeNoData
		v.Notes = append(v.Notes, "a execução não tem métricas")
		return v
	}
	secs := make([]second, len(windows))
	for i, w := range windows {
		secs[i] = classify(sc, slo, w)
	}
	last := float64(windows[len(windows)-1].T + 1)

	// Falhas aplicadas, em ordem. Uma falha ainda ativa (execução interrompida) vai até o fim.
	params := map[string]map[string]string{}
	for _, f := range sc.Faults {
		params[f.ID] = f.Params
	}
	var applied []store.FaultEvent
	for _, ev := range events {
		if ev.AppliedAt != nil {
			applied = append(applied, ev)
		}
	}
	sort.Slice(applied, func(i, j int) bool { return *applied[i].AppliedAt < *applied[j].AppliedAt })

	// busy marca segundos de falha ou de recuperação: ficam fora da linha de base.
	busy := make([]bool, len(secs))
	for i, ev := range applied {
		start := *ev.AppliedAt
		end := last
		if ev.RevertedAt != nil {
			end = *ev.RevertedAt
		}
		limit := last // a recuperação termina na próxima falha ou no fim
		if i+1 < len(applied) {
			limit = *applied[i+1].AppliedAt
		}
		r := FaultResult{FaultID: ev.FaultID, Type: ev.Type, Params: params[ev.FaultID], State: ev.State,
			Start: round1(start), End: round1(end)}
		for j, s := range secs {
			t := float64(s.w.T)
			// Um segundo conta para a falha se ela esteve ativa em pelo menos metade dele.
			if t+0.5 < start || t+0.5 >= end {
				continue
			}
			busy[j] = true
			r.During.add(s)
			worst(&r, s)
		}
		r.During.finish()
		// Recuperação: primeiro segundo depois do fim da falha a partir do qual há RecoveryStreak
		// segundos avaliados seguidos dentro dos SLOs.
		streak, from := 0, -1
		for j, s := range secs {
			t := float64(s.w.T)
			if t+0.5 < end || t >= limit {
				continue
			}
			busy[j] = true
			if !s.evaluated {
				continue
			}
			if s.ok {
				if streak == 0 {
					from = j
				}
				streak++
				if streak >= RecoveryStreak {
					break
				}
			} else {
				worst(&r, s)
				streak, from = 0, -1
			}
		}
		recovery := 0.0
		if streak >= RecoveryStreak && from >= 0 {
			rs := int(math.Max(0, math.Round(float64(secs[from].w.T)-end)))
			r.Recovered, r.RecoverySeconds = true, &rs
			recovery = recoveryScore(rs)
			// Depois de recuperado, o resto até a próxima falha volta a ser linha de base.
			for j := from + RecoveryStreak; j < len(secs) && float64(secs[j].w.T) < limit; j++ {
				busy[j] = false
			}
		} else if ev.RevertedAt == nil {
			v.Notes = append(v.Notes, fmt.Sprintf("a falha %s não foi revertida antes do fim; sem medida de recuperação", ev.Type))
		}
		r.Score = int(math.Round(100 * (WeightDuring*r.During.ratio() + WeightRecovery*recovery)))
		v.Faults = append(v.Faults, r)
	}

	for j, s := range secs {
		v.Overall.add(s)
		if !busy[j] {
			v.Baseline.add(s)
		}
	}
	v.Overall.finish()
	v.Baseline.finish()

	if v.Overall.Seconds == 0 {
		v.Grade = GradeNoData
		v.Notes = append(v.Notes, "nenhum segundo com requisições ou sonda para avaliar")
		return v
	}
	score := v.Baseline.ratio() * 100
	if len(v.Faults) > 0 {
		sum := 0.0
		for _, f := range v.Faults {
			sum += float64(f.Score)
		}
		score = WeightBaseline*v.Baseline.ratio()*100 + (1-WeightBaseline)*sum/float64(len(v.Faults))
	} else if sc.HasChaos() {
		v.Notes = append(v.Notes, "nenhuma falha chegou a ser aplicada; a nota considera só a linha de base")
	}
	v.Score = int(math.Round(score))
	switch {
	case v.Score >= 90:
		v.Grade = GradeResilient
	case v.Score >= 70:
		v.Grade = GradeWarning
	default:
		v.Grade = GradeFragile
	}
	v.Checks = checks(sc, slo, windows)
	return v
}

func worst(r *FaultResult, s second) {
	w := s.w
	if w.Stalled {
		r.StalledSeconds++
	}
	if w.P95 != nil && (r.WorstP95Ms == nil || *w.P95 > *r.WorstP95Ms) {
		v := *w.P95
		r.WorstP95Ms = &v
	}
	if w.ErrorRate != nil {
		e := round1(*w.ErrorRate * 100)
		if r.WorstErrorRatePct == nil || e > *r.WorstErrorRatePct {
			r.WorstErrorRatePct = &e
		}
	}
	if w.Probe != nil {
		if !w.Probe.OK {
			r.ProbeFailures++
		}
		if r.WorstP95Ms == nil || w.Probe.Ms > *r.WorstP95Ms {
			v := w.Probe.Ms
			r.WorstP95Ms = &v
		}
	}
}

func recoveryScore(s int) float64 {
	switch {
	case s <= RecoveryFast:
		return 1
	case s >= RecoverySlow:
		return 0
	default:
		return 1 - float64(s-RecoveryFast)/float64(RecoverySlow-RecoveryFast)
	}
}

// checks confere cada SLO na execução inteira.
func checks(sc scenario.Scenario, slo scenario.SLO, ws []load.Window) []Check {
	var out []Check
	if sc.HasLoad() {
		var p95ok, p95n, stalled, reqs, failed float64
		for _, w := range ws {
			reqs += w.RPS
			failed += w.RPS * load.Val(w.ErrorRate)
			switch {
			case w.Stalled || (w.RPS > 0 && w.P95 == nil):
				stalled++ // sem nenhuma resposta: fora do SLO de latência
			case w.P95 != nil:
				p95n++
				if *w.P95 <= slo.P95Ms {
					p95ok++
				}
			}
		}
		if total := p95n + stalled; total > 0 {
			pct := p95ok / total * 100
			actual := fmt.Sprintf("dentro em %s%% dos segundos", pt(pct, 1))
			if stalled > 0 {
				actual += fmt.Sprintf(" (%d s sem nenhuma resposta contam como fora)", int(stalled))
			}
			out = append(out, Check{Name: "p95 da carga", Target: fmt.Sprintf("≤ %s ms", pt(slo.P95Ms, 1)), Actual: actual, Pass: pct >= 99})
		}
		if reqs > 0 {
			e := failed / reqs * 100
			out = append(out, Check{Name: "taxa de erro da carga", Target: fmt.Sprintf("≤ %s%%", pt(slo.ErrorRatePct, 2)),
				Actual: fmt.Sprintf("%s%% no total", pt(e, 2)), Pass: e <= slo.ErrorRatePct})
		}
	}
	var probes, ok float64
	for _, w := range ws {
		if w.Probe != nil {
			probes++
			if w.Probe.OK {
				ok++
			}
		}
	}
	if probes > 0 {
		a := ok / probes * 100
		out = append(out, Check{Name: "disponibilidade da sonda", Target: fmt.Sprintf("≥ %s%%", pt(slo.AvailabilityPct, 2)),
			Actual: fmt.Sprintf("%s%%", pt(a, 1)), Pass: a >= slo.AvailabilityPct})
	}
	return out
}

// pt formata um número com vírgula decimal e sem zeros sobrando (ex.: 90,9 ou 500).
func pt(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.Replace(s, ".", ",", 1)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
