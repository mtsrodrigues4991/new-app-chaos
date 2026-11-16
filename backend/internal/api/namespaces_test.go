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

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// k8sChaos é um cenário de caos no Kubernetes com URL permitida; o que muda nos testes é
// onde as falhas são injetadas.
func k8sChaos(targetNS string, dep *scenario.Dependency) scenario.Scenario {
	return scenario.Scenario{
		Name:      "caos",
		Mode:      scenario.ModeChaos,
		Target:    scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: targetNS, Deployment: "api", BaseURL: "http://demo-api.demo:8080"},
		Profile:   scenario.Profile{Type: scenario.ProfileRamp, VUs: 1, DurationSeconds: 30},
		ProbePath: "/health",
		Faults:    []scenario.Fault{{ID: "f1", Type: scenario.FaultKillRestart, StartSeconds: 5, DurationSeconds: 10, Params: map[string]string{}, Dependency: dep}},
	}
}

func TestCheckNamespaces(t *testing.T) {
	t.Setenv("ALLOWED_TARGET_NAMESPACES", "demo, pix")
	cloud := GuardFromEnv()
	var local *Guard // modo local: sem lista
	coredns := &scenario.Dependency{Namespace: "kube-system", Deployment: "coredns"}
	for _, c := range []struct {
		name  string
		g     *Guard
		sc    scenario.Scenario
		wants string // "" = passa
	}{
		{"alvo permitido", cloud, k8sChaos("demo", nil), ""},
		{"dependência permitida", cloud, k8sChaos("demo", &scenario.Dependency{Namespace: "pix", Deployment: "pix-postgres"}), ""},
		{"alvo fora da lista", cloud, k8sChaos("outro", nil), "só são permitidos"},
		{"dependência fora da lista", cloud, k8sChaos("demo", &scenario.Dependency{Namespace: "outro", Deployment: "db"}), "só são permitidos"},
		// A brecha original: URL permitida e falha num namespace de sistema.
		{"dependência em kube-system", cloud, k8sChaos("demo", coredns), "de sistema"},
		{"alvo em kube-system", cloud, k8sChaos("kube-system", nil), "de sistema"},
		// Sem lista (KIND local) os namespaces de sistema continuam negados.
		{"local: qualquer namespace comum", local, k8sChaos("qualquer", nil), ""},
		{"local: kube-system", local, k8sChaos("demo", coredns), "de sistema"},
		{"local: entrada HTTPS", local, k8sChaos("traefik", nil), "de sistema"},
		{"local: motor de falhas", local, k8sChaos("demo", &scenario.Dependency{Namespace: "chaos-mesh", Deployment: "chaos-daemon"}), "de sistema"},
		{"local: o próprio app", local, k8sChaos("new-app-chaos", nil), "próprio app"},
		{"local: prefixo kube-", local, k8sChaos("kube-flannel", nil), "de sistema"},
	} {
		err := c.g.CheckNamespaces(c.sc, "new-app-chaos")
		switch {
		case c.wants == "" && err != nil:
			t.Errorf("%s: deveria passar: %v", c.name, err)
		case c.wants != "" && (err == nil || !strings.Contains(err.Error(), c.wants)):
			t.Errorf("%s: esperava erro com %q, veio %v", c.name, c.wants, err)
		}
	}
	// Alvo no Docker não tem namespace: a trava não se aplica.
	docker := scenario.Scenario{Target: scenario.Target{Container: "demo-api"}}
	if err := cloud.CheckNamespaces(docker, "new-app-chaos"); err != nil {
		t.Errorf("alvo no Docker: %v", err)
	}
}

// Pela API, como um usuário logado faria: URL base permitida e kill em kube-system. Tem que
// dar 400 ao salvar e também ao disparar um cenário que já estava salvo antes da trava.
func TestAPIRefusesChaosInSystemNamespace(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{Store: st, Kube: kube.NewStatic("http://127.0.0.1:1", "", "new-app-chaos"),
		Guard: &Guard{AllowedHosts: []string{"demo"}, AllowedNamespaces: []string{"demo"}}}
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

	bad := k8sChaos("demo", &scenario.Dependency{Namespace: "kube-system", Deployment: "coredns"})
	if code, body := do(http.MethodPost, "/api/scenarios", bad); code != http.StatusBadRequest || !strings.Contains(body, "kube-system") {
		t.Fatalf("salvar: esperava 400 citando kube-system, veio %d %s", code, body)
	}
	if list, _ := st.ListScenarios(context.Background()); len(list) != 0 {
		t.Fatalf("o cenário recusado não pode ter sido salvo: %+v", list)
	}

	// Cenário que já estava no banco (salvo antes de a trava existir).
	bad.ID = "antigo"
	if err := st.SaveScenario(context.Background(), bad); err != nil {
		t.Fatal(err)
	}
	if code, body := do(http.MethodPost, "/api/scenarios/antigo/runs", nil); code != http.StatusBadRequest || !strings.Contains(body, "kube-system") {
		t.Fatalf("disparar: esperava 400 citando kube-system, veio %d %s", code, body)
	}
	if code, body := do(http.MethodPut, "/api/scenarios/antigo", bad); code != http.StatusBadRequest {
		t.Fatalf("editar: esperava 400, veio %d %s", code, body)
	}
	if runs, _ := st.ListRuns(context.Background(), "", 10); len(runs) != 0 {
		t.Fatalf("nenhuma execução pode ter sido criada: %+v", runs)
	}

	// O mesmo cenário mirando só o namespace permitido é aceito.
	if code, body := do(http.MethodPost, "/api/scenarios", k8sChaos("demo", nil)); code != http.StatusCreated {
		t.Fatalf("cenário permitido: esperava 201, veio %d %s", code, body)
	}
}
