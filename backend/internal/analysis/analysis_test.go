package analysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ai"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/verdict"
)

type fake struct {
	got  ai.Request
	resp string
}

func (f *fake) Info() ai.ProviderInfo { return ai.ProviderInfo{ID: "fake"} }
func (f *fake) Generate(_ context.Context, r ai.Request) (ai.Response, error) {
	f.got = r
	return ai.Response{Text: f.resp, Model: "m"}, nil
}

func fp(v float64) *float64 { return &v }

func runData(id string, n int) RunData {
	sc := scenario.Scenario{ID: "sc", Name: "demo", Mode: scenario.ModeBoth,
		Faults: []scenario.Fault{{ID: "f1", Type: scenario.FaultKillRestart, StartSeconds: 10, DurationSeconds: 5}}}
	var ws []load.Window
	for i := 0; i < n; i++ {
		w := load.Window{T: int64(i), VUs: 10, RPS: 100, P50: fp(5), P95: fp(20), P99: fp(30), ErrorRate: fp(0), CPU: 12, MemMB: 30}
		if i >= 10 && i < 15 {
			w = load.Window{T: int64(i), VUs: 10, Stalled: true}
		}
		ws = append(ws, w)
	}
	evs := []store.FaultEvent{{FaultID: "f1", Type: scenario.FaultKillRestart, State: store.FaultReverted, AppliedAt: fp(10), RevertedAt: fp(15)}}
	start := time.Now()
	end := start.Add(time.Duration(n) * time.Second)
	run := store.Run{ID: id, ScenarioID: "sc", ScenarioName: "demo", Scenario: sc, Status: store.RunCompleted, CreatedAt: start, StartedAt: &start, EndedAt: &end}
	return RunData{Run: run, Windows: ws, Events: evs, Verdict: verdict.Evaluate(sc, ws, evs)}
}

func TestSeriesMarksFaultAndStall(t *testing.T) {
	csv := Series(runData("r", 30), MaxRows)
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if len(lines) != 31 {
		t.Fatalf("esperava cabeçalho + 30 linhas, veio %d", len(lines))
	}
	if !strings.HasPrefix(lines[12], "11,kill_restart,10,0,,,,,0,0,0,0,1,") {
		t.Fatalf("linha do segundo 11 inesperada: %s", lines[12])
	}
	grouped := Series(runData("r", 1300), MaxRows)
	if n := strings.Count(grouped, "\n"); n > MaxRows+2 || !strings.Contains(grouped, "blocos de 3 s") {
		t.Fatalf("esperava série agrupada em blocos de 3 s (%d linhas)", n)
	}
}

func TestAnalyzeWithReference(t *testing.T) {
	cur, ref := runData("atual", 30), runData("ref", 30)
	f := &fake{resp: `{"summary":"s","scoreExplanation":"e","breakingPoint":{"found":true,"description":"d"},
		"faultFindings":[{"faultId":"f1","finding":"x"}],"causes":["c"],"fixes":[{"title":"t","detail":"d"}],
		"regression":{"verdict":"igual","summary":"r","items":[]}}`}
	rep, err := Analyze(context.Background(), f, "m", Input{Current: cur, Reference: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Content.Regression == nil || rep.ReferenceRunID != "ref" || rep.Score != cur.Verdict.Score {
		t.Fatalf("relatório inesperado: %+v", rep)
	}
	props := f.got.Schema["properties"].(map[string]any)
	if _, ok := props["regression"]; !ok {
		t.Fatal("schema deveria pedir regressão quando há referência")
	}
	if !strings.Contains(f.got.Context, "Execução de referência") || !strings.Contains(f.got.Messages[0].Content, "referência") {
		t.Fatal("evidências deveriam trazer a referência")
	}
	b, _ := json.Marshal(f.got.Schema)
	if !strings.Contains(string(b), `"additionalProperties":false`) {
		t.Fatal("schema deveria fechar os objetos")
	}

	// Sem referência: nada de regressão, nem no schema nem no relatório.
	rep, err = Analyze(context.Background(), f, "m", Input{Current: cur})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.got.Schema["properties"].(map[string]any)["regression"]; ok || rep.Content.Regression != nil {
		t.Fatal("sem referência não deveria haver regressão")
	}
}

func TestAnalyzeWithCode(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "app.yaml"), []byte("a\nb\nc\nmaximum-pool-size: 10\n"), 0o644)
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>\n"), 0o644)
	snap := coderepo.Snapshot{Repo: "dono/app", Branch: "main", Commit: "abc", Root: root}
	b := coderepo.NewBrowser(snap)
	f := &fake{resp: `{"summary":"s","scoreExplanation":"e","breakingPoint":{"found":false,"description":"d"},
		"faultFindings":[],"causes":["c"],"fixes":[{"title":"Pool","detail":"d","file":"src/app.yml","line":4,"code":"maximum-pool-size: 20"},
		{"title":"Réplicas","detail":"d","file":"","line":0,"code":""},
		{"title":"Imagem","detail":"d","file":"Dockerfile","line":3,"code":"FROM x"},
		{"title":"Pom","detail":"d","file":"pom.xml","line":99,"code":"<dep/>"}]}`}
	rep, err := Analyze(context.Background(), f, "m", Input{Current: runData("r", 20), Code: b})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.got.Tools) != 3 || !strings.Contains(f.got.System, "dono/app") || !strings.Contains(f.got.System, "src/app.yaml") {
		t.Fatalf("deveria oferecer as 3 ferramentas e citar o repositório: %d", len(f.got.Tools))
	}
	fix := f.got.Schema["properties"].(map[string]any)["fixes"].(map[string]any)["items"].(map[string]any)
	if _, ok := fix["properties"].(map[string]any)["file"]; !ok {
		t.Fatal("schema deveria pedir file/line/code com o código disponível")
	}
	fx := rep.Content.Fixes
	if rep.Code == nil || rep.Code.Commit != "abc" || fx[1].URL != "" {
		t.Fatalf("relatório: %+v %+v", rep.Code, fx)
	}
	// .yml citado, mas o arquivo é .yaml: corrige o caminho e o link.
	if fx[0].File != "src/app.yaml" || fx[0].URL != "https://github.com/dono/app/blob/abc/src/app.yaml#L4" || fx[0].NewFile {
		t.Fatalf("correção do caminho: %+v", fx[0])
	}
	// Arquivo que não existe: sem link, marcado como novo.
	if fx[2].URL != "" || !fx[2].NewFile || fx[2].Line != 0 {
		t.Fatalf("arquivo novo: %+v", fx[2])
	}
	// Linha além do fim do arquivo: link sem âncora.
	if fx[3].Line != 0 || fx[3].URL != "https://github.com/dono/app/blob/abc/pom.xml" {
		t.Fatalf("linha fora do arquivo: %+v", fx[3])
	}
}

func TestActiveFaultLabelsDependency(t *testing.T) {
	at := func(v float64) *float64 { return &v }
	dep := &scenario.Dependency{Namespace: "pix", Deployment: "pix-postgres"}
	faults := []scenario.Fault{{ID: "a", Type: scenario.FaultLatency, Dependency: dep}, {ID: "b", Type: scenario.FaultPacketLoss, Dependency: dep, LinkOnly: true}, {ID: "c", Type: scenario.FaultDNSError}}
	evs := []store.FaultEvent{
		{FaultID: "a", Type: scenario.FaultLatency, AppliedAt: at(0), RevertedAt: at(5)},
		{FaultID: "b", Type: scenario.FaultPacketLoss, AppliedAt: at(10), RevertedAt: at(15)},
		{FaultID: "c", Type: scenario.FaultDNSError, AppliedAt: at(20), RevertedAt: at(25)},
	}
	for ts, want := range map[int64]string{2: "latency@pix/pix-postgres", 12: "packet_loss>pix/pix-postgres", 22: "dns_error", 30: ""} {
		if got := activeFault(evs, faults, ts, 60); got != want {
			t.Errorf("t=%d: %q, quer %q", ts, got, want)
		}
	}
}

func TestDescribeDiagnostics(t *testing.T) {
	var lines []store.LogLine
	lines = append(lines, store.LogLine{T: 0, Text: "Started PixApplication in 3.2 seconds"})
	for i := 0; i < 200; i++ {
		lines = append(lines, store.LogLine{T: float64(i) / 10, Text: "GET /api/v1/pix-keys/" + strings.Repeat("7", i%3+1) + " 200"})
	}
	for i := 0; i < 50; i++ {
		lines = append(lines, store.LogLine{T: 111 + float64(i)/10, Text: "HikariPool-1 - Connection is not available, request timed out after 3000ms (total=10)"})
	}
	lines = append(lines, store.LogLine{T: 112, Text: "\tat com.zaxxer.hikari.pool.HikariPool.getConnection(HikariPool.java:181)"})
	d := &store.Diagnostics{
		Logs:   []store.PodLog{{Namespace: "pix", Pod: "pix-api-0", Container: "pix-api", Role: "alvo", Lines: lines}},
		Events: []store.KubeEvent{{T: 110.5, Type: "Normal", Reason: "Killing", Namespace: "pix", Object: "Pod/pix-postgres-0", Message: "Stopping container"}},
		Notes:  []string{"eventos de x: proibido"},
	}
	out := DescribeDiagnostics(d, DiagChars)
	for _, want := range []string{"t=110.5 Normal Killing pix/Pod/pix-postgres-0", "t=111 HikariPool-1 - Connection is not available", "[x50 até t=115.9]",
		"HikariPool.getConnection", "Started PixApplication", "alvo pix/pix-api-0 (container pix-api)", "proibido"} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q em:\n%s", want, out)
		}
	}
	if strings.Count(out, "pix-keys") > 25 {
		t.Fatalf("linhas comuns deviam ter sido cortadas:\n%s", out)
	}
	if small := DescribeDiagnostics(d, 300); len(small) > 900 || !strings.Contains(small, "omitidas") {
		t.Fatalf("orçamento não respeitado (%d):\n%s", len(small), small)
	}
	if DescribeDiagnostics(nil, DiagChars) != "" {
		t.Fatal("sem diagnóstico, nada")
	}
}

func TestDescribeApp(t *testing.T) {
	a := &store.AppTelemetry{
		Metrics: []store.MetricSeries{
			{Service: "pix/pix-api", Key: "heap_mb", Label: "Heap usado", Unit: "MB", Points: []store.PointValue{{T: 0, V: 120.5}, {T: 5, V: 130}}},
			{Service: "pix/pix-api", Key: "db_pending", Label: "Esperando conexão do pool", Points: []store.PointValue{{T: 5, V: 8}}},
		},
		Traces: []store.TraceSummary{{Service: "pix/pix-api", Kind: "erro", TraceID: "e1", T: 21, Root: "GET /x", DurationMs: 3000,
			Spans: []store.SpanSummary{{Service: "pix-api", Name: "getConnection", DurationMs: 2999, Error: true, Detail: "SQLTransientConnectionException"}}}},
		Profiles:  []store.ProfileTop{{Service: "pix/pix-api", Type: "cpu", Functions: []store.ProfileEntry{{Name: "a.B.c", SelfPct: 42}}}},
		Available: map[string][]string{"pix/pix-api": {"jvm_memory_used_bytes"}},
		Notes:     []string{"pix/pix-postgres não mandou métricas"},
	}
	out := DescribeDiagnostics(&store.Diagnostics{App: a}, DiagChars)
	for _, want := range []string{"t,heap_mb,db_pending\n0,120.5,\n5,130,8\n", "t=21 erro pix/pix-api: GET /x, 3000 ms (trace e1)",
		"[erro] pix-api getConnection: 2999 ms (SQLTransientConnectionException)", "pix/pix-api, cpu: a.B.c 42%", "jvm_memory_used_bytes", "não mandou métricas"} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q em:\n%s", want, out)
		}
	}
}
