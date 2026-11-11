package load

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Snapshot é o resumo de um segundo que o dashboard embutido do k6 publica.
type Snapshot struct {
	T                int64 // segundos desde o início do teste no k6
	P50, P95, P99    float64
	Requests         float64
	ErrorRate        float64
	S2xx, S3xx, S4xx float64
	S5xx, SNone      float64
	VUs              float64
	HasVUs           bool
}

// DashboardReader interpreta o stream SSE em /events do dashboard do k6 (k6 0.54).
//
// O formato é interno do k6: eventos "metric" anunciam nomes de métricas e eventos
// "snapshot" trazem um array de valores por métrica, na ordem alfabética dos nomes
// já anunciados. Cada tipo tem seus agregados (anunciados no evento "param"):
// counter [count, rate], gauge [value], rate [rate], trend [avg, max, med, min, p90, p95, p99].
type DashboardReader struct {
	names   map[string]bool
	startMs float64
	trend   []string
	// Origin, quando definido, é o início da execução: os segundos passam a contar dele, e não
	// do início do k6 (que sobe uns segundos depois). Assim carga, falhas e CPU ficam alinhadas.
	Origin time.Time
}

func NewDashboardReader() *DashboardReader {
	return &DashboardReader{names: map[string]bool{}, trend: []string{"avg", "max", "med", "min", "p(90)", "p(95)", "p(99)"}}
}

// Event processa um evento SSE. Devolve o resumo quando é um snapshot, e stop=true no fim do teste.
func (d *DashboardReader) Event(name string, data []byte) (snap *Snapshot, stop bool, err error) {
	switch name {
	case "param":
		var p struct {
			Aggregates map[string][]string `json:"aggregates"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, false, err
		}
		if t := p.Aggregates["trend"]; len(t) > 0 {
			d.trend = t
		}
	case "metric":
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, false, err
		}
		for k := range m {
			d.names[k] = true
		}
	case "start":
		var v [][]float64
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, false, err
		}
		if len(v) > 0 && len(v[0]) > 0 {
			d.startMs = v[0][0]
		}
	case "stop":
		return nil, true, nil
	case "snapshot":
		var vals [][]float64
		if err := json.Unmarshal(data, &vals); err != nil {
			return nil, false, err
		}
		names := make([]string, 0, len(d.names))
		for k := range d.names {
			names = append(names, k)
		}
		sort.Strings(names)
		if len(names) != len(vals) {
			return nil, false, fmt.Errorf("snapshot com %d métricas, esperado %d", len(vals), len(names))
		}
		by := make(map[string][]float64, len(names))
		for i, n := range names {
			by[n] = vals[i]
		}
		return d.build(by), false, nil
	}
	return nil, false, nil
}

func (d *DashboardReader) build(by map[string][]float64) *Snapshot {
	at := func(metric string, i int) float64 {
		if v := by[metric]; i < len(v) {
			return v[i]
		}
		return 0
	}
	trendIdx := func(stat string) int {
		for i, s := range d.trend {
			if s == stat {
				return i
			}
		}
		return -1
	}
	s := &Snapshot{
		Requests:  at("http_reqs", 0),
		ErrorRate: at("http_req_failed", 0),
		S2xx:      at("status_2xx", 0),
		S3xx:      at("status_3xx", 0),
		S4xx:      at("status_4xx", 0),
		S5xx:      at("status_5xx", 0),
		SNone:     at("status_none", 0),
	}
	if i := trendIdx("med"); i >= 0 {
		s.P50 = at("http_req_duration", i)
	}
	if i := trendIdx("p(95)"); i >= 0 {
		s.P95 = at("http_req_duration", i)
	}
	if i := trendIdx("p(99)"); i >= 0 {
		s.P99 = at("http_req_duration", i)
	}
	if v := by["vus"]; len(v) > 0 {
		s.VUs, s.HasVUs = v[0], true
	}
	// "time" é o fim do período de 1 s; o período começa 1 s antes.
	origin := d.startMs
	if !d.Origin.IsZero() {
		origin = float64(d.Origin.UnixMilli())
	}
	if t := at("time", 0); t > 0 && origin > 0 {
		s.T = int64((t-origin)/1000) - 1
		if s.T < 0 {
			s.T = 0
		}
	}
	return s
}

// ReadSSE lê um stream Server-Sent Events e chama onEvent para cada evento completo.
// Para quando onEvent devolve false, o stream termina ou ctx é cancelado.
func ReadSSE(r io.Reader, onEvent func(name string, data []byte) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var name string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 || name != "" {
				if !onEvent(name, data) {
					return nil
				}
			}
			name, data = "", nil
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(line[5:], " ")...)
		}
	}
	return sc.Err()
}

// StreamDashboard conecta no /events do dashboard do k6 e entrega os resumos até o fim do teste.
// origin é o início da execução (ver DashboardReader.Origin); zero = início do k6.
func StreamDashboard(ctx context.Context, url string, origin time.Time, onSnapshot func(*Snapshot)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dashboard do k6 respondeu %d", resp.StatusCode)
	}
	d := NewDashboardReader()
	d.Origin = origin
	var procErr error
	err = ReadSSE(resp.Body, func(name string, data []byte) bool {
		snap, stop, err := d.Event(name, data)
		if err != nil {
			procErr = err
			return false
		}
		if snap != nil {
			onSnapshot(snap)
		}
		return !stop
	})
	if procErr != nil {
		return procErr
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}
