package load

import (
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Formato real do /events do k6 0.54 com taxa de chegada (capturado, abreviado nas métricas
// que não usamos): o "cumulative" vem antes de cada "snapshot" e o último chega antes do "stop".
// As métricas ep_1_* e dropped_iterations só são anunciadas quando recebem a primeira amostra.
const cumulativeSSE = `event: param
data: {"period":1000,"aggregates":{"counter":["count","rate"],"gauge":["value"],"rate":["rate"],"trend":["avg","max","med","min","p(90)","p(95)","p(99)"]}}

event: metric
data: {"time":{"type":"gauge"}}

event: start
data: [[1000000]]

event: metric
data: {"ep_0_duration":{"type":"trend"},"ep_0_reqs":{"type":"counter"},"http_req_duration":{"type":"trend"},"http_req_failed":{"type":"rate"},"http_reqs":{"type":"counter"},"steady_duration":{"type":"trend"},"steady_reqs":{"type":"counter"},"vus":{"type":"gauge"}}

event: cumulative
data: [[2,9,1.5,1,3,4,8],[100,10],[5,90,2,1,9,20,80],[0.1],[1000,100],[2,9,1.5,1,3,4,8],[100,10],[1010000],[3]]

event: snapshot
data: [[2,9,1.5,1,3,4,8],[100,100],[5,90,2,1,9,20,80],[0.1],[100,100],[2,9,1.5,1,3,4,8],[100,100],[1010000],[3]]

event: metric
data: {"dropped_iterations":{"type":"counter"},"ep_1_duration":{"type":"trend"},"ep_1_errs":{"type":"counter"},"ep_1_reqs":{"type":"counter"},"steady_errs":{"type":"counter"}}

event: cumulative
data: [[7,2],[2.5,9.126,1.75,1,3.5,4.4,8.8],[1500,50],[40,400,30,10,90,120.456,300],[50,1],[500,10],[6.2,400,2.1,1,12,25,95],[0.05],[4000,133.3],[10.5,400,2.5,1,20,48.25,150],[50,2.5],[2000,100],[1030000],[]]

event: snapshot
data: [[7,7],[2,9,1.5,1,3,4,8],[100,100],[40,400,30,10,90,120,300],[5,5],[50,50],[5,90,2,1,9,20,80],[0.1],[150,150],[10,400,2,1,20,48,150],[5,5],[150,150],[1030000],[]]

event: stop
data: [[1030000]]

`

var twoEndpoints = scenario.Scenario{
	Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/items"}, {Method: "POST", Path: "/orders"}},
	Profile:   scenario.Profile{Type: scenario.ProfilePlateau, VUs: 50, Rate: 200, DurationSeconds: 30, WarmupSeconds: 10},
}

func readTotals(t *testing.T, sse string) *Totals {
	t.Helper()
	d := NewDashboardReader()
	err := ReadSSE(strings.NewReader(sse), func(name string, data []byte) bool {
		_, stop, err := d.Event(name, data)
		if err != nil {
			t.Fatal(err)
		}
		return !stop
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals() == nil {
		t.Fatal("nenhum acumulado lido")
	}
	return d.Totals()
}

// O resumo usa o último acumulado: percentis do k6 sobre todas as requisições, e não a
// mediana dos p95 de cada segundo.
func TestSummaryFromCumulative(t *testing.T) {
	s := readTotals(t, cumulativeSSE).Summary(twoEndpoints)

	if !s.OpenModel || s.TargetRate != 200 || s.WarmupSeconds != 10 {
		t.Fatalf("modelo errado: %+v", s)
	}
	// Execução inteira: 4000 requisições em 30 s, 5% de erro, p95 de 25 ms.
	o := s.Overall
	if o.Requests != 4000 || o.Seconds != 30 || o.RPS != 133.3 || o.ErrorRatePct != 5 || o.P95Ms != 25 || o.P99Ms != 95 || o.P50Ms != 2.1 || o.MaxMs != 400 {
		t.Fatalf("execução inteira errada: %+v", o)
	}
	// Janela estável: só depois dos 10 s de aquecimento, então 2000 requisições em 20 s.
	st := s.Steady
	if st.Requests != 2000 || st.Seconds != 20 || st.RPS != 100 || st.ErrorRatePct != 2.5 || st.P95Ms != 48.25 {
		t.Fatalf("janela estável errada: %+v", st)
	}
	if len(s.Endpoints) != 2 {
		t.Fatalf("esperava 2 endpoints, veio %+v", s.Endpoints)
	}
	// Uma correção no endpoint lento tem que aparecer no endpoint lento: os números não se misturam.
	fast, slow := s.Endpoints[0], s.Endpoints[1]
	if fast.Path != "/items" || fast.Requests != 1500 || fast.P95Ms != 4.4 || fast.ErrorRatePct != 0 || fast.RPS != 75 {
		t.Fatalf("endpoint rápido errado: %+v", fast)
	}
	if slow.Path != "/orders" || slow.Method != "POST" || slow.Requests != 500 || slow.P95Ms != 120.46 || slow.ErrorRatePct != 10 {
		t.Fatalf("endpoint lento errado: %+v", slow)
	}
	// Gerador saturado: a carga entregue foi menor que a pedida, então não serve para comparar.
	if s.DroppedIterations != 7 || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "7 requisições") {
		t.Fatalf("esperava aviso de 7 iterações descartadas: %+v", s)
	}
}

func TestSummaryWarnings(t *testing.T) {
	clean := strings.Replace(cumulativeSSE, `"dropped_iterations":{"type":"counter"},`, "", 1)
	clean = strings.Replace(clean, "data: [[7,2],", "data: [", 1)
	clean = strings.Replace(clean, "data: [[7,7],", "data: [", 1)
	if s := readTotals(t, clean).Summary(twoEndpoints); len(s.Warnings) != 0 || s.DroppedIterations != 0 {
		t.Fatalf("taxa de chegada sem descarte não tem aviso: %+v", s.Warnings)
	}

	closed := twoEndpoints
	closed.Profile.Rate = 0
	s := readTotals(t, clean).Summary(closed)
	if s.OpenModel || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "modelo fechado") {
		t.Fatalf("carga em VUs tem que avisar que não serve para comparar: %+v", s.Warnings)
	}

	// Além do limite, os endpoints ficam só nos totais, e o resumo diz quantos.
	many := twoEndpoints
	many.Endpoints = make([]scenario.Endpoint, MaxMeasuredEndpoints+3)
	if s := readTotals(t, clean).Summary(many); len(s.Endpoints) != MaxMeasuredEndpoints || s.UnmeasuredEndpoints != 3 {
		t.Fatalf("esperava %d medidos e 3 de fora: %d e %d", MaxMeasuredEndpoints, len(s.Endpoints), s.UnmeasuredEndpoints)
	}
}

// Um stream sem acumulado (k6 morto antes do primeiro período) não tem resumo.
func TestNoCumulativeNoTotals(t *testing.T) {
	d := NewDashboardReader()
	_, _, _ = d.Event("metric", []byte(`{"http_reqs":{},"time":{}}`))
	if _, _, err := d.Event("snapshot", []byte(`[[1,1],[1001000]]`)); err != nil || d.Totals() != nil {
		t.Fatalf("snapshot não é acumulado: %v %v", err, d.Totals())
	}
	if _, _, err := d.Event("cumulative", []byte(`[[1,1]]`)); err == nil {
		t.Fatal("esperava erro quando o número de métricas do acumulado não bate")
	}
}
