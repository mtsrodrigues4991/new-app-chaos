package load

import (
	"strings"
	"testing"
	"time"
)

// Trecho real do /events do dashboard do k6 0.54 (abreviado nas métricas que não usamos).
const sampleSSE = `event: param
data: {"period":1000,"aggregates":{"counter":["count","rate"],"gauge":["value"],"rate":["rate"],"trend":["avg","max","med","min","p(90)","p(95)","p(99)"]}}

event: start
data: [[1000000]]

event: metric
data: {"http_req_duration":{"type":"trend"},"http_req_failed":{"type":"rate"},"http_reqs":{"type":"counter"},"status_2xx":{"type":"counter"},"status_4xx":{"type":"counter"},"time":{"type":"gauge"}}

event: snapshot
data: [[0.24,6.2,0.22,0.05,0.34,0.4,0.68],[0.19665],[4602,4602],[3696,3696],[904,904],[1001000]]

event: metric
data: {"vus":{"type":"gauge"},"vus_max":{"type":"gauge"}}

event: snapshot
data: [[0.25,9.4,0.2,0.04,0.43,0.56,1.15],[0.2],[10029,10029],[7947,7947],[2084,2084],[1002000],[4],[20]]

event: stop
data: [[1002500]]

event: snapshot
data: [[1],[1],[1,1],[1,1],[1,1],[1003000],[1],[1]]

`

func TestDashboardToWindows(t *testing.T) {
	d := NewDashboardReader()
	var snaps []*Snapshot
	stopped := false
	err := ReadSSE(strings.NewReader(sampleSSE), func(name string, data []byte) bool {
		s, stop, err := d.Event(name, data)
		if err != nil {
			t.Fatal(err)
		}
		if s != nil {
			snaps = append(snaps, s)
		}
		stopped = stop
		return !stop
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || len(snaps) != 2 {
		t.Fatalf("esperava 2 snapshots e parar no stop; veio %d, stop=%v", len(snaps), stopped)
	}

	start := time.Now()
	a := NewAggregator(start)
	for _, s := range snaps {
		a.AddSnapshot(s)
	}
	a.AddStats(start.Add(1500*time.Millisecond), 55.55, 128)
	a.AddStats(start.Add(5*time.Second), 10, 10) // sem resumo do k6 nesse segundo: fica de fora
	ws := a.Flush(MaxSecond)
	if len(ws) != 2 {
		t.Fatalf("esperava 2 janelas, veio %+v", ws)
	}
	w0, w1 := ws[0], ws[1]
	if w0.T != 0 || w0.RPS != 4602 || Val(w0.P50) != 0.2 || Val(w0.P95) != 0.4 || Val(w0.P99) != 0.7 || w0.S2xx != 3696 || w0.S4xx != 904 || w0.VUs != 0 {
		t.Fatalf("janela 0 errada: %+v", w0)
	}
	if w1.T != 1 || w1.VUs != 4 || w1.CPU != 55.6 || w1.MemMB != 128 || Val(w1.ErrorRate) != 0.2 {
		t.Fatalf("janela 1 errada: %+v", w1)
	}
}

func TestSnapshotMismatchIsError(t *testing.T) {
	d := NewDashboardReader()
	_, _, _ = d.Event("metric", []byte(`{"http_reqs":{},"time":{}}`))
	if _, _, err := d.Event("snapshot", []byte(`[[1,1]]`)); err == nil {
		t.Fatal("esperava erro quando o número de métricas não bate")
	}
}

func TestStalledSecondHasNoLatency(t *testing.T) {
	start := time.Now()
	a := NewAggregator(start)
	a.AddSnapshot(&Snapshot{T: 0, Requests: 50, P95: 12, VUs: 5, HasVUs: true})
	a.AddSnapshot(&Snapshot{T: 1, Requests: 0, P95: 0, VUs: 5, HasVUs: true}) // alvo morto: nada concluiu
	ws := a.Flush(MaxSecond)
	if len(ws) != 2 {
		t.Fatalf("esperava 2 janelas, veio %+v", ws)
	}
	if ws[0].Stalled || ws[0].P95 == nil {
		t.Fatalf("janela 0 deveria ter latência e não estar presa: %+v", ws[0])
	}
	if !ws[1].Stalled || ws[1].P95 != nil || ws[1].ErrorRate != nil {
		t.Fatalf("janela 1 deveria estar presa e sem latência: %+v", ws[1])
	}
}

func TestDashboardOriginShiftsSeconds(t *testing.T) {
	d := NewDashboardReader()
	// O k6 começou 2 s depois do início da execução.
	origin := time.UnixMilli(1_000_000)
	d.Origin = origin
	_, _, _ = d.Event("metric", []byte(`{"http_reqs":{},"time":{}}`))
	_, _, _ = d.Event("start", []byte(`[[1002000]]`))
	s, _, err := d.Event("snapshot", []byte(`[[10,10],[1004000]]`))
	if err != nil {
		t.Fatal(err)
	}
	// Período de 1 s que termina em 4 s desde o início da execução: segundo 3.
	if s.T != 3 {
		t.Fatalf("esperava T=3 contado do início da execução, veio %d", s.T)
	}
}

func TestAllTimeoutsHaveNoLatency(t *testing.T) {
	a := NewAggregator(time.Now())
	a.AddSnapshot(&Snapshot{T: 0, Requests: 20, SNone: 20, ErrorRate: 1, P95: 0, VUs: 20, HasVUs: true})
	w := a.Flush(MaxSecond)[0]
	if w.P95 != nil || Val(w.ErrorRate) != 1 || w.Stalled {
		t.Fatalf("segundo só com timeouts: latência nula, erro 100%%, não preso; veio %+v", w)
	}
}
