package load

import (
	"fmt"
	"math"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Summary são os números da execução inteira, lidos do acumulado que o k6 publica. É o que
// serve para comparar duas execuções: os percentis são calculados pelo k6 sobre todas as
// requisições, e não sobre os percentis de cada segundo.
type Summary struct {
	// OpenModel indica carga por taxa de chegada. Só nesse modelo a vazão não depende da latência.
	OpenModel bool `json:"openModel"`
	// TargetRate é a taxa pedida no pico, em requisições por segundo (modelo aberto).
	TargetRate int `json:"targetRate,omitempty"`
	// Overall cobre todas as requisições, inclusive as do aquecimento.
	Overall Segment `json:"overall"`
	// WarmupSeconds é o aquecimento do cenário; Steady cobre só as requisições iniciadas depois dele.
	WarmupSeconds int     `json:"warmupSeconds"`
	Steady        Segment `json:"steady"`
	// Endpoints são os números da janela estável por endpoint, na ordem do cenário.
	Endpoints []EndpointSummary `json:"endpoints"`
	// UnmeasuredEndpoints são os endpoints além de MaxMeasuredEndpoints (só nos totais).
	UnmeasuredEndpoints int `json:"unmeasuredEndpoints,omitempty"`
	// DroppedIterations são as requisições que o k6 deixou de disparar por falta de VU livre
	// (modelo aberto). Acima de zero, a carga entregue foi menor que a pedida.
	DroppedIterations int `json:"droppedIterations,omitempty"`
	// Warnings dizem por que a execução não serve para comparação (vazio = serve).
	Warnings []string `json:"warnings,omitempty"`
}

// Segment são os números de um trecho da execução. Latências em ms, contando todas as
// requisições do trecho (inclusive as que falharam), como o http_req_duration do k6.
type Segment struct {
	Seconds      float64 `json:"seconds"`
	Requests     int     `json:"requests"`
	RPS          float64 `json:"rps"`
	ErrorRatePct float64 `json:"errorRatePct"`
	AvgMs        float64 `json:"avgMs"`
	P50Ms        float64 `json:"p50Ms"`
	P90Ms        float64 `json:"p90Ms"`
	P95Ms        float64 `json:"p95Ms"`
	P99Ms        float64 `json:"p99Ms"`
	MaxMs        float64 `json:"maxMs"`
}

// EndpointSummary são os números de um endpoint na janela estável.
type EndpointSummary struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Segment
}

// Totals é o último acumulado publicado pelo dashboard do k6 (evento "cumulative").
type Totals struct {
	by    map[string][]float64
	trend []string
	// ElapsedSeconds vai do início do teste no k6 até este acumulado.
	ElapsedSeconds float64
}

func (t *Totals) at(metric string, i int) float64 {
	if v := t.by[metric]; i >= 0 && i < len(v) {
		return v[i]
	}
	return 0
}

func (t *Totals) stat(metric, stat string) float64 {
	for i, s := range t.trend {
		if s == stat {
			return round2(t.at(metric, i))
		}
	}
	return 0
}

// segment monta um trecho a partir de um trend de duração e dos contadores de requisições e erros.
func (t *Totals) segment(duration string, requests, errors, seconds float64) Segment {
	s := Segment{
		Seconds:  round1(seconds),
		Requests: int(math.Round(requests)),
		AvgMs:    t.stat(duration, "avg"),
		P50Ms:    t.stat(duration, "med"),
		P90Ms:    t.stat(duration, "p(90)"),
		P95Ms:    t.stat(duration, "p(95)"),
		P99Ms:    t.stat(duration, "p(99)"),
		MaxMs:    t.stat(duration, "max"),
	}
	if requests > 0 {
		s.ErrorRatePct = round2(errors / requests * 100)
		if seconds > 0 {
			s.RPS = round1(requests / seconds)
		}
	}
	return s
}

// Summary monta o resumo da execução do cenário sc.
func (t *Totals) Summary(sc scenario.Scenario) Summary {
	p := sc.Profile
	out := Summary{OpenModel: p.OpenModel(), WarmupSeconds: p.WarmupSeconds, Endpoints: []EndpointSummary{}}
	if out.OpenModel {
		out.TargetRate = p.Rate
	}
	reqs := t.at("http_reqs", 0)
	out.Overall = t.segment("http_req_duration", reqs, reqs*t.at("http_req_failed", 0), t.ElapsedSeconds)

	steadySeconds := math.Max(0, t.ElapsedSeconds-float64(p.WarmupSeconds))
	out.Steady = t.segment(metricSteadyDuration, t.at(metricSteadyReqs, 0), t.at(metricSteadyErrs, 0), steadySeconds)
	for i, e := range sc.Endpoints {
		if i >= MaxMeasuredEndpoints {
			out.UnmeasuredEndpoints = len(sc.Endpoints) - MaxMeasuredEndpoints
			break
		}
		seg := t.segment(endpointMetric(i, "duration"), t.at(endpointMetric(i, "reqs"), 0), t.at(endpointMetric(i, "errs"), 0), steadySeconds)
		out.Endpoints = append(out.Endpoints, EndpointSummary{Method: e.Method, Path: e.Path, Segment: seg})
	}

	out.DroppedIterations = int(math.Round(t.at(metricDropped, 0)))
	if out.DroppedIterations > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"o gerador não deu conta da taxa pedida: %d requisições deixaram de ser disparadas por falta de VU livre. A carga entregue foi menor que a pedida; aumente os VUs pré-alocados (hoje %d) e rode de novo antes de comparar.",
			out.DroppedIterations, p.VUs))
	}
	if !out.OpenModel {
		out.Warnings = append(out.Warnings, "carga em VUs (modelo fechado): a vazão muda quando a latência muda. Para comparar duas versões da aplicação, use a carga por taxa de chegada.")
	}
	if out.Steady.Requests == 0 {
		out.Warnings = append(out.Warnings, "nenhuma requisição na janela estável (a execução terminou antes do fim do aquecimento).")
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
