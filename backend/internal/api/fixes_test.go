package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/analysis"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/fix"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ghpr"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

type offPublisher struct{}

func (offPublisher) Configured() bool { return false }
func (offPublisher) OpenDraftPR(context.Context, ghpr.Request) (ghpr.PR, error) {
	return ghpr.PR{}, ghpr.ErrNotConfigured
}

// Pela API: a tentativa nasce da correção do relatório, com o commit que a IA leu e o build do
// cenário, e fica esperando aprovação. Sem o token de escrita, aprovar é recusado.
func TestFixFromReport(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	srv := &Server{Store: st, Fixes: &fix.Manager{Store: st, Publisher: offPublisher{}}}
	h := srv.Router()
	do := func(method, path string, body any) (int, string) {
		var buf bytes.Buffer
		if body != nil {
			json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.String()
	}

	sc := scenario.Scenario{ID: "sc", Name: "pix", Code: &scenario.CodeRepo{Repo: "dono/repo", Branch: "master"}}
	st.SaveScenario(ctx, sc)
	st.CreateRun(ctx, store.Run{ID: "r1", ScenarioID: "sc", Scenario: sc, Status: store.RunCompleted, CreatedAt: time.Now()})
	if code, body := do("POST", "/api/runs/r1/fixes", map[string]int{"index": 0}); code != http.StatusConflict || !strings.Contains(body, "análise") {
		t.Fatalf("sem análise: %d %s", code, body)
	}

	yes, no := true, false
	rep := analysis.Report{
		Code: &analysis.CodeInfo{Snapshot: coderepo.Snapshot{Repo: "dono/repo", Branch: "master", Commit: "0123456789abcdef0123456789abcdef01234567", Pinned: true}},
		Content: analysis.Content{Fixes: []analysis.Fix{
			{Title: "Aumenta o pool", Detail: "d", Metric: "p95", Expected: "menos de 300 ms", Applies: &yes, Diff: "--- a/src/application.yaml\n+++ b/src/application.yaml\n@@ -1 +1 @@\n-pool: 5\n+pool: 20\n"},
			{Title: "Não aplica", Detail: "d", Applies: &no, ApplyError: "contexto diferente", Diff: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"},
		}},
	}
	rep.Usage.Model = "claude-opus-5-5"
	st.SaveAnalysis(ctx, "r1", rep)

	if code, body := do("POST", "/api/runs/r1/fixes", map[string]int{"index": 1}); code != http.StatusConflict || !strings.Contains(body, "não se aplica") {
		t.Fatalf("patch que não aplica: %d %s", code, body)
	}
	if code, body := do("POST", "/api/runs/r1/fixes", map[string]int{"index": 5}); code != http.StatusBadRequest {
		t.Fatalf("índice inexistente: %d %s", code, body)
	}
	// O cenário ainda não diz como testar o repositório.
	if code, body := do("POST", "/api/runs/r1/fixes", map[string]int{"index": 0}); code != http.StatusBadRequest || !strings.Contains(body, "comando de teste") {
		t.Fatalf("sem build configurado: %d %s", code, body)
	}
	// Preenchido depois da execução, no cenário atual: vale.
	sc.Code.TestImage, sc.Code.TestCommand = "maven:3.9-eclipse-temurin-17", "mvn -B -q verify"
	st.SaveScenario(ctx, sc)
	code, body := do("POST", "/api/runs/r1/fixes", map[string]int{"index": 0})
	if code != http.StatusCreated {
		t.Fatalf("criar: %d %s", code, body)
	}
	var a fix.Attempt
	json.Unmarshal([]byte(body), &a)
	if a.Status != fix.AwaitingApproval || a.Commit != rep.Code.Commit || a.BaseBranch != "master" || a.TestCommand != "mvn -B -q verify" || a.Model != "claude-opus-5-5" || a.Metric != "p95" {
		t.Fatalf("tentativa: %+v", a)
	}

	code, body = do("GET", "/api/fixes/"+a.ID, nil)
	if code != http.StatusOK || !strings.Contains(body, `"canDecide":true`) || !strings.Contains(body, "PR_GITHUB_TOKEN") {
		t.Fatalf("a tela precisa saber o que falta para aprovar: %d %s", code, body)
	}
	if code, body := do("POST", "/api/fixes/"+a.ID+"/approve", nil); code != http.StatusPreconditionFailed || !strings.Contains(body, "PR_GITHUB_TOKEN") {
		t.Fatalf("aprovar sem token: %d %s", code, body)
	}
	if code, _ := do("POST", "/api/fixes/"+a.ID+"/reject", nil); code != http.StatusOK {
		t.Fatalf("recusar: %d", code)
	}
	if code, _ := do("POST", "/api/fixes/"+a.ID+"/approve", nil); code != http.StatusConflict {
		t.Fatalf("aprovar depois de recusada: %d", code)
	}
	if code, body := do("GET", "/api/fixes?runId=r1", nil); code != http.StatusOK || !strings.Contains(body, a.ID) {
		t.Fatalf("lista: %d %s", code, body)
	}
}
