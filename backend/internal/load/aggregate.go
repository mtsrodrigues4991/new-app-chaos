package load

import (
	"math"
	"sort"
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
	// Probe é o resultado da sonda (modo só caos): uma requisição por segundo.
	Probe *Probe `json:"probe,omitempty"`
}

// Probe é uma requisição de sondagem ao endpoint escolhido.
type Probe struct {
	Ms     float64 `json:"ms"`              // tempo até a resposta (ou até o erro)
	Status int     `json:"status"`          // código HTTP; 0 = sem resposta
	OK     bool    `json:"ok"`              // 2xx ou 3xx
	Error  string  `json:"error,omitempty"` // ex.: connection refused, timeout
}

type bucket struct {
	w        Window
	hasLoad  bool
	hasVUs   bool
	hasStats bool
	hasProbe bool
}

// Aggregator junta os resumos do k6 e as estatísticas do container em janelas de 1 segundo.
// Não é seguro para uso concorrente: quem chama deve proteger com mutex.
type Aggregator struct {
	start   time.Time
	buckets map[int64]*bucket
	lastVUs int
}

func NewAggregator(start time.Time) *Aggregator {
	return &Aggregator{start: start, buckets: map[int64]*bucket{}}
}

func (a *Aggregator) bucket(sec int64) *bucket {
	if sec < 0 {
		sec = 0
	}
	b, ok := a.buckets[sec]
	if !ok {
		b = &bucket{w: Window{T: sec}}
		a.buckets[sec] = b
	}
	return b
}

// AddSnapshot registra o resumo de um segundo vindo do k6.
func (a *Aggregator) AddSnapshot(s *Snapshot) {
	b := a.bucket(s.T)
	b.hasLoad = true
	b.w.P50, b.w.P95, b.w.P99 = round1(s.P50), round1(s.P95), round1(s.P99)
	b.w.RPS = math.Round(s.Requests)
	b.w.ErrorRate = s.ErrorRate
	b.w.S2xx, b.w.S3xx, b.w.S4xx = int(math.Round(s.S2xx)), int(math.Round(s.S3xx)), int(math.Round(s.S4xx))
	b.w.S5xx, b.w.SErr = int(math.Round(s.S5xx)), int(math.Round(s.SNone))
	if s.HasVUs {
		b.w.VUs, b.hasVUs = int(s.VUs), true
	}
}

// AddStats registra CPU e memória do container alvo num instante.
func (a *Aggregator) AddStats(ts time.Time, cpu, memMB float64) {
	b := a.bucket(int64(ts.Sub(a.start) / time.Second))
	b.w.CPU, b.w.MemMB, b.hasStats = round1(cpu), round1(memMB), true
}

// AddProbe registra a sonda disparada no instante ts.
func (a *Aggregator) AddProbe(ts time.Time, p Probe) {
	b := a.bucket(int64(ts.Sub(a.start) / time.Second))
	p.Ms = round1(p.Ms)
	b.w.Probe, b.hasProbe = &p, true
}

// Flush devolve, em ordem, as janelas anteriores a "before" (em segundos desde o início)
// e as remove do agregador. Use before = MaxSecond para esvaziar tudo.
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
		if !b.hasLoad && !b.hasProbe {
			// Segundo sem resumo do k6 nem sonda (antes do primeiro ou depois do último período):
			// mostrar latência e RPS zerados seria enganoso, então a janela fica de fora.
			continue
		}
		if b.hasVUs {
			a.lastVUs = b.w.VUs
		} else {
			b.w.VUs = a.lastVUs
		}
		out = append(out, b.w)
	}
	return out
}

// Elapsed devolve os segundos inteiros desde o início.
func (a *Aggregator) Elapsed(now time.Time) int64 { return int64(now.Sub(a.start) / time.Second) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// MaxSecond é o valor para Flush esvaziar todas as janelas.
const MaxSecond = math.MaxInt64
