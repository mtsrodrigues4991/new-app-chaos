// Package analysis monta as evidências de uma execução para a IA e define o relatório e o chat.
// Não depende de provedor: fala com internal/ai.
package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/verdict"
)

// MaxRows limita as linhas da série por segundo enviadas ao modelo; execuções mais longas
// são agrupadas em blocos de vários segundos.
const MaxRows = 600

// RunData é tudo que se sabe de uma execução.
type RunData struct {
	Run     store.Run
	Windows []load.Window
	Events  []store.FaultEvent
	Verdict verdict.Verdict
	// Diagnostics são os logs e eventos coletados (nil em execuções antigas).
	Diagnostics *store.Diagnostics
}

// Stats é o resumo numérico de uma execução.
type Stats struct {
	DurationSeconds   int      `json:"durationSeconds"`
	TotalRequests     float64  `json:"totalRequests,omitempty"`
	ErrorRatePct      *float64 `json:"errorRatePct,omitempty"`
	P95MedianMs       *float64 `json:"p95MedianMs,omitempty"`
	P95MaxMs          *float64 `json:"p95MaxMs,omitempty"`
	P99MaxMs          *float64 `json:"p99MaxMs,omitempty"`
	MaxRPS            float64  `json:"maxRps,omitempty"`
	PeakVUs           int      `json:"peakVus,omitempty"`
	StalledSeconds    int      `json:"stalledSeconds,omitempty"`
	Status4xx         int      `json:"status4xx,omitempty"`
	Status5xx         int      `json:"status5xx,omitempty"`
	NoResponse        int      `json:"noResponse,omitempty"`
	CPUAvgPct         float64  `json:"cpuAvgPct"`
	CPUMaxPct         float64  `json:"cpuMaxPct"`
	MemAvgMB          float64  `json:"memAvgMb"`
	MemMaxMB          float64  `json:"memMaxMb"`
	ProbeAvailability *float64 `json:"probeAvailabilityPct,omitempty"`
	ProbeMaxMs        *float64 `json:"probeMaxMs,omitempty"`
}

// Summarize calcula o resumo numérico.
func Summarize(ws []load.Window) Stats {
	var s Stats
	if len(ws) == 0 {
		return s
	}
	s.DurationSeconds = int(ws[len(ws)-1].T + 1)
	var failed, cpuSum, memSum float64
	var p95s []float64
	var probes, probeOK int
	for _, w := range ws {
		s.TotalRequests += w.RPS
		failed += w.RPS * load.Val(w.ErrorRate)
		if w.P95 != nil {
			p95s = append(p95s, *w.P95)
			s.P95MaxMs = maxp(s.P95MaxMs, *w.P95)
		}
		if w.P99 != nil {
			s.P99MaxMs = maxp(s.P99MaxMs, *w.P99)
		}
		s.MaxRPS = math.Max(s.MaxRPS, w.RPS)
		if w.VUs > s.PeakVUs {
			s.PeakVUs = w.VUs
		}
		if w.Stalled {
			s.StalledSeconds++
		}
		s.Status4xx += w.S4xx
		s.Status5xx += w.S5xx
		s.NoResponse += w.SErr
		cpuSum += w.CPU
		memSum += w.MemMB
		s.CPUMaxPct = math.Max(s.CPUMaxPct, w.CPU)
		s.MemMaxMB = math.Max(s.MemMaxMB, w.MemMB)
		if w.Probe != nil {
			probes++
			if w.Probe.OK {
				probeOK++
			}
			s.ProbeMaxMs = maxp(s.ProbeMaxMs, w.Probe.Ms)
		}
	}
	n := float64(len(ws))
	s.CPUAvgPct, s.MemAvgMB = r1(cpuSum/n), r1(memSum/n)
	if s.TotalRequests > 0 {
		e := r2(failed / s.TotalRequests * 100)
		s.ErrorRatePct = &e
	}
	if len(p95s) > 0 {
		sort.Float64s(p95s)
		m := p95s[len(p95s)/2]
		s.P95MedianMs = &m
	}
	if probes > 0 {
		a := r1(float64(probeOK) / float64(probes) * 100)
		s.ProbeAvailability = &a
	}
	return s
}

func maxp(cur *float64, v float64) *float64 {
	if cur == nil || v > *cur {
		return &v
	}
	return cur
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }
func r2(v float64) float64 { return math.Round(v*100) / 100 }

func jsonText(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(erro ao serializar: %v)", err)
	}
	return string(b)
}

func num(p *float64) string {
	if p == nil {
		return ""
	}
	return trimFloat(*p)
}

func trimFloat(v float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0"), ".")
}

// activeFault devolve o tipo da falha ativa no segundo t ("" se nenhuma).
// Quando a falha foi aplicada numa dependência, o rótulo leva o nome dela (ex.: latency@pix/pix-postgres,
// ou latency>pix/pix-postgres quando só o caminho até ela foi atingido).
func activeFault(events []store.FaultEvent, faults []scenario.Fault, t int64, end float64) string {
	for _, ev := range events {
		if ev.AppliedAt == nil {
			continue
		}
		stop := end
		if ev.RevertedAt != nil {
			stop = *ev.RevertedAt
		}
		if float64(t)+0.5 >= *ev.AppliedAt && float64(t)+0.5 < stop {
			label := string(ev.Type)
			for _, f := range faults {
				if f.ID == ev.FaultID && f.Dependency != nil {
					sep := "@"
					if f.LinkOnly {
						sep = ">"
					}
					label += sep + f.Dependency.Name()
				}
			}
			return label
		}
	}
	return ""
}

// Series escreve a série por segundo em CSV. Acima de maxRows linhas, agrupa em blocos:
// latências e CPU/memória pelo pior valor do bloco, RPS pela média, contagens somadas.
func Series(d RunData, maxRows int) string {
	ws := d.Windows
	if len(ws) == 0 {
		return "(sem métricas)"
	}
	hasLoad, hasProbe, hasPods := d.Run.Scenario.HasLoad(), false, false
	for _, w := range ws {
		hasProbe = hasProbe || w.Probe != nil
		hasPods = hasPods || w.Pods != nil
	}
	step := 1
	if len(ws) > maxRows {
		step = int(math.Ceil(float64(len(ws)) / float64(maxRows)))
	}
	end := float64(ws[len(ws)-1].T + 1)
	var b strings.Builder
	if step > 1 {
		fmt.Fprintf(&b, "Agrupado em blocos de %d s (t = início do bloco; latência, CPU e memória = pior valor; rps = média; contagens somadas).\n", step)
	}
	cols := []string{"t", "falha"}
	if hasLoad {
		cols = append(cols, "vus", "rps", "p50_ms", "p95_ms", "p99_ms", "erro_pct", "s2xx", "s4xx", "s5xx", "sem_resposta", "presa")
	}
	if hasProbe {
		cols = append(cols, "sonda_ms", "sonda_status", "sonda_erro")
	}
	cols = append(cols, "cpu_pct", "mem_mb")
	if hasPods {
		cols = append(cols, "pods_prontos", "pods_total")
	}
	b.WriteString(strings.Join(cols, ",") + "\n")
	for i := 0; i < len(ws); i += step {
		grp := ws[i:min(i+step, len(ws))]
		g := grp[0]
		var rps, errSum float64
		var vus, s2, s4, s5, se, stalled int
		var p50, p95, p99 *float64
		var cpu, mem float64
		var probe *load.Probe
		var pods *load.PodCount // pior momento do bloco: menos pods prontos
		faults := map[string]bool{}
		for _, w := range grp {
			rps += w.RPS
			errSum += w.RPS * load.Val(w.ErrorRate)
			vus = max(vus, w.VUs)
			s2, s4, s5, se = s2+w.S2xx, s4+w.S4xx, s5+w.S5xx, se+w.SErr
			if w.Stalled {
				stalled++
			}
			if w.P50 != nil {
				p50 = maxp(p50, *w.P50)
			}
			if w.P95 != nil {
				p95 = maxp(p95, *w.P95)
			}
			if w.P99 != nil {
				p99 = maxp(p99, *w.P99)
			}
			cpu, mem = math.Max(cpu, w.CPU), math.Max(mem, w.MemMB)
			if w.Probe != nil && (probe == nil || !w.Probe.OK || w.Probe.Ms > probe.Ms) {
				p := *w.Probe
				probe = &p
			}
			if w.Pods != nil && (pods == nil || w.Pods.Ready < pods.Ready) {
				pc := *w.Pods
				pods = &pc
			}
			if f := activeFault(d.Events, d.Run.Scenario.Faults, w.T, end); f != "" {
				faults[f] = true
			}
		}
		fl := make([]string, 0, len(faults))
		for f := range faults {
			fl = append(fl, f)
		}
		sort.Strings(fl)
		row := []string{fmt.Sprint(g.T), strings.Join(fl, "+")}
		if hasLoad {
			errPct := ""
			if rps > 0 {
				errPct = trimFloat(r2(errSum / rps * 100))
			}
			presa := ""
			if stalled > 0 {
				presa = fmt.Sprint(stalled)
			}
			row = append(row, fmt.Sprint(vus), trimFloat(r1(rps/float64(len(grp)))), num(p50), num(p95), num(p99), errPct,
				fmt.Sprint(s2), fmt.Sprint(s4), fmt.Sprint(s5), fmt.Sprint(se), presa)
		}
		if hasProbe {
			if probe != nil {
				row = append(row, trimFloat(probe.Ms), fmt.Sprint(probe.Status), strings.ReplaceAll(probe.Error, ",", ";"))
			} else {
				row = append(row, "", "", "")
			}
		}
		row = append(row, trimFloat(cpu), trimFloat(mem))
		if hasPods {
			if pods != nil {
				row = append(row, fmt.Sprint(pods.Ready), fmt.Sprint(pods.Total))
			} else {
				row = append(row, "", "")
			}
		}
		b.WriteString(strings.Join(row, ",") + "\n")
	}
	return b.String()
}

func duration(r store.Run) string {
	if r.StartedAt == nil || r.EndedAt == nil {
		return "?"
	}
	return r.EndedAt.Sub(*r.StartedAt).Round(time.Second).String()
}

// Describe escreve as evidências completas de uma execução.
// diagChars é o orçamento dos logs e eventos (0 = não mandar).
func Describe(title string, d RunData, maxRows, diagChars int) string {
	var b strings.Builder
	r := d.Run
	fmt.Fprintf(&b, "## %s\n\n", title)
	fmt.Fprintf(&b, "- id: %s\n- cenário: %s\n- modo: %s\n- status: %s\n", r.ID, r.ScenarioName, r.Scenario.EffectiveMode(), r.Status)
	fmt.Fprintf(&b, "- alvo: %s (%s)\n", r.Scenario.Target.Name(), r.Scenario.Target.EffectivePlatform())
	if r.Error != "" {
		fmt.Fprintf(&b, "- erro da execução: %s\n", r.Error)
	}
	fmt.Fprintf(&b, "- criada em: %s\n- duração real: %s\n\n", r.CreatedAt.Format(time.RFC3339), duration(r))
	fmt.Fprintf(&b, "### Cenário como estava ao rodar\n```json\n%s\n```\n\n", jsonText(r.Scenario))
	fmt.Fprintf(&b, "### Veredito calculado pelo app (determinístico)\n```json\n%s\n```\n\n", jsonText(d.Verdict))
	fmt.Fprintf(&b, "### Falhas: instantes reais (segundos desde o início)\n```json\n%s\n```\n\n", jsonText(d.Events))
	fmt.Fprintf(&b, "### Resumo numérico\n```json\n%s\n```\n\n", jsonText(Summarize(d.Windows)))
	fmt.Fprintf(&b, "### Série por segundo (CSV)\n```csv\n%s```\n", Series(d, maxRows))
	if diagChars > 0 && d.Diagnostics != nil {
		b.WriteString("\n" + DescribeDiagnostics(d.Diagnostics, diagChars))
	}
	return b.String()
}

// HistoryRow é uma linha do histórico do cenário mandado ao chat.
type HistoryRow struct {
	RunID     string        `json:"runId"`
	CreatedAt time.Time     `json:"createdAt"`
	Status    string        `json:"status"`
	Score     int           `json:"score"`
	Grade     verdict.Grade `json:"grade"`
	Reference bool          `json:"reference,omitempty"`
	Current   bool          `json:"current,omitempty"`
	Faults    []string      `json:"faults,omitempty"`
	Stats     Stats         `json:"stats"`
}

// HistoryRowOf resume uma execução para o histórico.
func HistoryRowOf(d RunData) HistoryRow {
	h := HistoryRow{RunID: d.Run.ID, CreatedAt: d.Run.CreatedAt, Status: string(d.Run.Status),
		Score: d.Verdict.Score, Grade: d.Verdict.Grade, Stats: Summarize(d.Windows)}
	for _, f := range d.Run.Scenario.Faults {
		h.Faults = append(h.Faults, string(f.Type))
	}
	return h
}
