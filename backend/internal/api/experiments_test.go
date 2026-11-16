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

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/experiment"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

type noRuns struct{}

func (noRuns) Start(context.Context, scenario.Scenario) (store.Run, error) {
	return store.Run{}, context.Canceled // nenhum teste daqui chega a rodar
}
func (noRuns) Abort(context.Context, string) error { return nil }

// A API recusa, antes de rodar qualquer coisa, um experimento que não pode dar um resultado
// válido ou que mira onde não deve.
func TestCreateExperimentValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{Store: st, Kube: kube.NewStatic("http://127.0.0.1:1", "", "new-app-chaos"),
		Guard: &Guard{AllowedNamespaces: []string{"demo"}}, Experiments: experiment.NewManager(noRuns{}, st)}
	h := srv.Router()
	post := func(body any) (int, string) {
		var buf bytes.Buffer
		json.NewEncoder(&buf).Encode(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/experiments", &buf))
		return rec.Code, rec.Body.String()
	}
	sc := scenario.Scenario{
		ID: "sc", Name: "demo", Mode: scenario.ModeLoad,
		Target:    scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "demo", Deployment: "api", BaseURL: "http://api.demo:8080"},
		Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/items", Weight: 1}},
		Profile:   scenario.Profile{Type: scenario.ProfilePlateau, VUs: 20, DurationSeconds: 30},
	}
	st.SaveScenario(context.Background(), sc)

	for name, c := range map[string]struct {
		body  any
		wants string
	}{
		"sem cenário":  {experiment.Spec{}, "escolha o cenário"},
		"carga em VUs": {experiment.Spec{ScenarioID: "sc"}, "taxa de chegada"},
		"B em namespace de sistema": {experiment.Spec{ScenarioID: "sc",
			B: &scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "kube-system", Deployment: "x", BaseURL: "http://api.demo:8080"}}, "alvo B"},
		"B fora da lista": {experiment.Spec{ScenarioID: "sc",
			B: &scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "outro", Deployment: "x", BaseURL: "http://api.demo:8080"}}, "alvo B"},
	} {
		if code, body := post(c.body); code != http.StatusBadRequest || !strings.Contains(body, c.wants) {
			t.Errorf("%s: esperava 400 com %q, veio %d %s", name, c.wants, code, body)
		}
	}
	if list, _ := srv.Experiments.List(context.Background(), "", 10); len(list) != 0 {
		t.Fatalf("nenhum experimento recusado pode ter sido criado: %+v", list)
	}
}
