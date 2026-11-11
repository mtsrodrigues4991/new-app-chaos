package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCollect(t *testing.T) {
	started := time.Unix(1_800_000_000, 0)
	ended := started.Add(60 * time.Second)
	var queries []string
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}
	mux.HandleFunc("/api/v1/label/job/values", func(w http.ResponseWriter, r *http.Request) { ok(w, []string{"pix/pix-postgres", "pix/pix-api"}) })
	mux.HandleFunc("/api/v1/label/__name__/values", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("match[]") != `{job="pix/pix-api"}` {
			t.Errorf("match[]=%q", r.URL.Query().Get("match[]"))
		}
		ok(w, []string{"jvm_memory_used_bytes", "db_client_connections_usage"})
	})
	mux.HandleFunc("/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		if strings.Contains(q, "jvm_memory_used_bytes") && strings.Contains(q, `job="pix/pix-api"`) {
			ok(w, map[string]any{"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]any{},
				"values": [][]any{{float64(started.Unix() + 5), "100"}, {float64(started.Unix() + 10), "NaN"}}}}})
			return
		}
		ok(w, map[string]any{"resultType": "matrix", "result": []any{}})
	})
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if !strings.Contains(q, `resource.service.name = "pix-api"`) {
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []any{}})
			return
		}
		ns := fmt.Sprint(started.Add(21 * time.Second).UnixNano())
		if strings.Contains(q, "status = error") {
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []any{
				map[string]any{"traceID": "n0", "rootServiceName": "pix-api", "rootTraceName": "pixdb", "startTimeUnixNano": ns}, // ruído: 0 ms
				map[string]any{"traceID": "e1", "rootServiceName": "pix-api", "rootTraceName": "GET /api/v1/pix-keys/{id}", "startTimeUnixNano": ns, "durationMs": 3000}}})
			return
		}
		if st := r.URL.Query().Get("start"); st >= fmt.Sprint(started.Add(50*time.Second).Unix()) { // janela da 2ª falha
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []any{map[string]any{"traceID": "w2", "rootServiceName": "pix-api", "rootTraceName": "GET",
				"startTimeUnixNano": fmt.Sprint(started.Add(56 * time.Second).UnixNano()), "durationMs": 700}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"traces": []any{
			map[string]any{"traceID": "e1", "rootServiceName": "pix-api", "rootTraceName": "GET", "startTimeUnixNano": ns, "durationMs": 3000},
			map[string]any{"traceID": "s1", "rootServiceName": "pix-api", "rootTraceName": "GET", "startTimeUnixNano": ns, "durationMs": 900}}})
	})
	mux.HandleFunc("/api/traces/e1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"pix-api"}}]},
		 "scopeSpans":[{"spans":[
		  {"name":"GET /api/v1/pix-keys/{id}","startTimeUnixNano":"1000000000","endTimeUnixNano":"4000000000","status":{}},
		  {"name":"HikariDataSource.getConnection","startTimeUnixNano":"1000000000","endTimeUnixNano":"3999000000","status":{"code":2},
		   "events":[{"name":"exception","attributes":[{"key":"exception.type","value":{"stringValue":"java.sql.SQLTransientConnectionException"}},{"key":"exception.message","value":{"stringValue":"Connection is not available"}}]}]},
		  {"name":"SELECT pixdb.pix_key","startTimeUnixNano":"1000000000","endTimeUnixNano":"1002000000","attributes":[{"key":"db.statement","value":{"stringValue":"select * from pix_key where id=?"}}]}
		 ]}]}]}`))
	})
	mux.HandleFunc("/api/traces/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"batches":[]}`)) })
	mux.HandleFunc("/pyroscope/render", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), `service_name="pix-api"`) || !strings.HasPrefix(r.URL.Query().Get("query"), "process_cpu") {
			_, _ = w.Write([]byte(`{"flamebearer":{"names":[],"levels":[],"numTicks":0}}`))
			return
		}
		// total 100: main(self 0) -> a (self 60), b (self 30) ; c (self 10) sob b
		_, _ = w.Write([]byte(`{"flamebearer":{"names":["total","main","a","b","c"],"numTicks":100,
		 "levels":[[0,100,0,0],[0,100,0,1],[0,60,60,2,0,40,30,3],[60,10,10,4]]}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &Client{Prometheus: srv.URL, Tempo: srv.URL, Pyroscope: srv.URL, Grafana: "http://localhost:3001"}
	windows := []Window{{started.Add(20 * time.Second), started.Add(40 * time.Second)}, {started.Add(55 * time.Second), started.Add(70 * time.Second)}}
	app := c.Collect(context.Background(), []Service{{"pix", "pix-api"}, {"pix", "nada"}}, started, ended, windows)

	if len(app.Metrics) != 1 || app.Metrics[0].Key != "heap_mb" || len(app.Metrics[0].Points) != 1 || app.Metrics[0].Points[0].T != 5 || app.Metrics[0].Points[0].V != 100 {
		t.Fatalf("métricas=%+v", app.Metrics)
	}
	if got := app.Available["pix/pix-api"]; len(got) != 2 {
		t.Fatalf("available=%v", app.Available)
	}
	if !strings.Contains(strings.Join(app.Notes, "|"), "pix/nada não mandou métricas") {
		t.Fatalf("notes=%v", app.Notes)
	}
	// e1 com erro (o n0 de 0 ms é ruído), s1 lento da 1ª falha, w2 lento da 2ª, em ordem de t.
	if len(app.Traces) != 3 || app.Traces[0].TraceID != "e1" || app.Traces[0].Kind != "erro" || app.Traces[0].T != 21 ||
		app.Traces[1].TraceID != "s1" || app.Traces[1].Kind != "lento" || app.Traces[2].TraceID != "w2" || app.Traces[2].T != 56 {
		t.Fatalf("traces=%+v", app.Traces)
	}
	sp := app.Traces[0].Spans
	if len(sp) != 3 || !sp[0].Error || !strings.Contains(sp[0].Detail, "SQLTransientConnectionException: Connection is not available") || sp[1].DurationMs != 3000 || sp[2].Detail != "select * from pix_key where id=?" {
		t.Fatalf("spans=%+v", sp)
	}
	if len(app.Profiles) != 1 || app.Profiles[0].Type != "cpu" || app.Profiles[0].Functions[0].Name != "a" || app.Profiles[0].Functions[0].SelfPct != 60 || len(app.Profiles[0].Functions) != 3 {
		t.Fatalf("profiles=%+v", app.Profiles)
	}
	for _, q := range queries {
		if strings.Contains(q, "$SEL") {
			t.Fatalf("seletor não substituído: %s", q)
		}
	}
}

func TestTopSelfSkipsAgent(t *testing.T) {
	// total 100: app (self 50) e o exportador do agente (self 10) com musl embaixo (self 40).
	names := []string{"total", "com/pix/App.run", "io/opentelemetry/exporter/Exporter.export", "musl_memcpy"}
	levels := [][]int64{{0, 100, 0, 0}, {0, 50, 50, 1, 0, 50, 10, 2}, {50, 40, 40, 3}}
	top := TopSelf(names, levels, 100, 10)
	if len(top) != 1 || top[0].Name != "com/pix/App.run" || top[0].SelfPct != 50 {
		t.Fatalf("top=%+v", top)
	}
}
