package load

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"time"
)

// Window são as métricas agregadas de um segundo da execução.
type Window struct {
	T         int64   `json:"t"`         // segundos desde o início da execução
	P50       float64 `json:"p50"`       // ms
	P95       float64 `json:"p95"`       // ms
	P99       float64 `json:"p99"`       // ms
	RPS       float64 `json:"rps"`       // requisições concluídas no segundo
	ErrorRate float64 `json:"errorRate"` // 0 a 1
	S2xx      int     `json:"s2xx"`
	S3xx      int     `json:"s3xx"`
	S4xx      int     `json:"s4xx"`
	S5xx      int     `json:"s5xx"`
	SErr      int     `json:"sErr"` // sem resposta (timeout, conexão recusada)
	VUs       int     `json:"vus"`
	CPU       float64 `json:"cpu"`   // % de CPU do container alvo (100 = 1 núcleo)
	MemMB     float64 `json:"memMb"` // memória do container alvo
}

type bucket struct {
	durations []float64
	reqs      int
	failed    int
	s2, s3    int
	s4, s5    int
	sErr      int
	vus       int
	hasVUs    bool
	cpu       float64
	mem       float64
	hasStats  bool
}

// Aggregator junta os pontos do k6 e as estatísticas do container em janelas de 1 segundo.
// Não é seguro para uso concorrente: quem chama deve proteger com mutex.
type Aggregator struct {
	start   time.Time
	buckets map[int64]*bucket
	lastVUs int
}

func NewAggregator(start time.Time) *Aggregator {
	return &Aggregator{start: start, buckets: map[int64]*bucket{}}
}

func (a *Aggregator) bucketAt(ts time.Time) *bucket {
	sec := int64(ts.Sub(a.start) / time.Second)
	if sec < 0 {
		sec = 0
	}
	b, ok := a.buckets[sec]
	if !ok {
		b = &bucket{}
		a.buckets[sec] = b
	}
	return b
}

// k6Line é uma linha do --out json do k6.
type k6Line struct {
	Type   string `json:"type"`
	Metric string `json:"metric"`
	Data   struct {
		Time  time.Time         `json:"time"`
		Value float64           `json:"value"`
		Tags  map[string]string `json:"tags"`
	} `json:"data"`
}

// AddK6Line processa uma linha JSON do k6. Linhas que não são pontos são ignoradas.
func (a *Aggregator) AddK6Line(line []byte) {
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var l k6Line
	if err := json.Unmarshal(line, &l); err != nil || l.Type != "Point" {
		return
	}
	b := a.bucketAt(l.Data.Time)
	switch l.Metric {
	case "http_req_duration":
		b.durations = append(b.durations, l.Data.Value)
	case "http_reqs":
		b.reqs++
		code, _ := strconv.Atoi(l.Data.Tags["status"])
		switch {
		case code >= 500:
			b.s5++
		case code >= 400:
			b.s4++
		case code >= 300:
			b.s3++
		case code >= 200:
			b.s2++
		default:
			b.sErr++
		}
	case "http_req_failed":
		if l.Data.Value != 0 {
			b.failed++
		}
	case "vus":
		b.vus = int(l.Data.Value)
		b.hasVUs = true
	}
}

// AddStats registra CPU e memória do container alvo num instante.
func (a *Aggregator) AddStats(ts time.Time, cpu, memMB float64) {
	b := a.bucketAt(ts)
	b.cpu, b.mem, b.hasStats = cpu, memMB, true
}

// Flush devolve, em ordem, as janelas anteriores a "before" (em segundos desde o início)
// e as remove do agregador. Use before = math.MaxInt64 para esvaziar tudo.
func (a *Aggregator) Flush(before int64) []Window {
	var secs []int64
	for s := range a.buckets {
		if s < before {
			secs = append(secs, s)
		}
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
	out := make([]Window, 0, len(secs))
	for _, s := range secs {
		b := a.buckets[s]
		delete(a.buckets, s)
		if b.hasVUs {
			a.lastVUs = b.vus
		}
		w := Window{
			T: s, RPS: float64(b.reqs), VUs: a.lastVUs,
			S2xx: b.s2, S3xx: b.s3, S4xx: b.s4, S5xx: b.s5, SErr: b.sErr,
			CPU: round1(b.cpu), MemMB: round1(b.mem),
		}
		if b.reqs > 0 {
			w.ErrorRate = float64(b.failed) / float64(b.reqs)
		}
		if len(b.durations) > 0 {
			sort.Float64s(b.durations)
			w.P50 = round1(percentile(b.durations, 0.50))
			w.P95 = round1(percentile(b.durations, 0.95))
			w.P99 = round1(percentile(b.durations, 0.99))
		}
		out = append(out, w)
	}
	return out
}

// Elapsed devolve os segundos inteiros desde o início.
func (a *Aggregator) Elapsed(now time.Time) int64 { return int64(now.Sub(a.start) / time.Second) }

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// MaxSecond é o valor para Flush esvaziar todas as janelas.
const MaxSecond = math.MaxInt64
