package load

import (
	"fmt"
	"testing"
	"time"
)

func point(metric string, ts time.Time, value float64, status string) []byte {
	return []byte(fmt.Sprintf(`{"type":"Point","metric":%q,"data":{"time":%q,"value":%v,"tags":{"status":%q}}}`,
		metric, ts.Format(time.RFC3339Nano), value, status))
}

func TestAggregator(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := NewAggregator(start)
	t0 := start.Add(200 * time.Millisecond)
	for i := 1; i <= 100; i++ {
		a.AddK6Line(point("http_req_duration", t0, float64(i), "200"))
		status := "200"
		failed := 0.0
		if i > 90 {
			status, failed = "500", 1
		}
		a.AddK6Line(point("http_reqs", t0, 1, status))
		a.AddK6Line(point("http_req_failed", t0, failed, status))
	}
	a.AddK6Line(point("vus", t0, 10, ""))
	a.AddK6Line([]byte("lixo que não é json"))
	a.AddStats(start.Add(1500*time.Millisecond), 55.55, 128)

	ws := a.Flush(1)
	if len(ws) != 1 {
		t.Fatalf("esperava 1 janela, veio %d", len(ws))
	}
	w := ws[0]
	if w.RPS != 100 || w.P50 != 50 || w.P95 != 95 || w.P99 != 99 {
		t.Fatalf("percentis/RPS errados: %+v", w)
	}
	if w.S2xx != 90 || w.S5xx != 10 || w.ErrorRate != 0.1 || w.VUs != 10 {
		t.Fatalf("contagens erradas: %+v", w)
	}

	rest := a.Flush(MaxSecond)
	if len(rest) != 1 || rest[0].T != 1 || rest[0].CPU != 55.6 || rest[0].VUs != 10 {
		t.Fatalf("segunda janela errada: %+v", rest)
	}
}
