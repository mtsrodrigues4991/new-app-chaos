package fix

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ephemeral"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/experiment"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ghpr"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/sandbox"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
)

type fakeBuilder struct {
	mu       sync.Mutex
	builds   []ghpr.Build
	comments []string
	failTag  string
}

func (f *fakeBuilder) BuildImage(_ context.Context, b ghpr.Build, _ time.Duration) (ghpr.BuildRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, b)
	if f.failTag != "" && strings.HasPrefix(b.Tag, f.failTag) {
		return ghpr.BuildRun{ID: 9, URL: "https://ci/9", Status: "completed", Conclusion: "failure"}, ghpr.ErrBuildFailed
	}
	return ghpr.BuildRun{ID: int64(len(f.builds)), URL: "https://ci/ok", Status: "completed", Conclusion: "success"}, nil
}

func (f *fakeBuilder) Comment(_ context.Context, repo string, n int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, body)
	return nil
}

type fakeEnvs struct {
	mu      sync.Mutex
	created []string // "id imagemA imagemB"
	deleted []string
	err     error
}

func (f *fakeEnvs) Ready() string { return "" }
func (f *fakeEnvs) Create(_ context.Context, id string, src scenario.Target, a, b string) (ephemeral.Env, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, id+" "+a+" "+b)
	if f.err != nil {
		return ephemeral.Env{}, f.err
	}
	t := func(arm string) scenario.Target {
		return scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "efemero", Deployment: "nac-" + id + "-" + arm, BaseURL: "http://nac-" + id + "-" + arm + ".efemero:8080"}
	}
	return ephemeral.Env{ID: id, A: t("a"), B: t("b")}, nil
}
func (f *fakeEnvs) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeExperiments struct {
	mu      sync.Mutex
	spec    experiment.Spec
	sc      scenario.Scenario
	outcome stats.Outcome
	status  experiment.Status
}

func (f *fakeExperiments) Start(_ context.Context, spec experiment.Spec, sc scenario.Scenario) (experiment.Experiment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spec, f.sc = spec, sc
	return experiment.Experiment{ID: "exp1", Spec: spec, Status: experiment.Running}, nil
}
func (f *fakeExperiments) Get(_ context.Context, id string) (experiment.Experiment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	if st == "" {
		st = experiment.Completed
	}
	e := experiment.Experiment{ID: id, Spec: f.spec, Status: st, Runs: make([]experiment.ArmRun, 10)}
	if st == experiment.Completed {
		noise := 2.0
		e.Result = &experiment.Result{Outcome: f.outcome, NoisePct: &noise, Notes: []string{"Métrica principal: o intervalo de 95% exclui zero para o lado bom."},
			Primary: stats.Comparison{NA: 5, NB: 5, MedianA: 800, MedianB: 240, Diff: -560, DiffPct: -70, Lo: -590, Hi: -530, Outcome: f.outcome},
			Guards:  []experiment.Guard{{Metric: experiment.MetricErrorRate, Comparison: stats.Comparison{Outcome: stats.NoDifference}}}}
	} else {
		e.Error = "nenhuma execução valeu"
	}
	return e, nil
}

type fakeScenarios struct{ sc scenario.Scenario }

func (f fakeScenarios) GetScenario(context.Context, string) (scenario.Scenario, error) {
	return f.sc, nil
}

func k8sScenario() scenario.Scenario {
	return scenario.Scenario{ID: "sc", Name: "pix", Mode: scenario.ModeLoad,
		Target:    scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "pix", Deployment: "pix-api", BaseURL: "http://pix-api.pix:8080"},
		Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/api/v1/pix-keys/x", Weight: 1}},
		Profile:   scenario.Profile{Type: scenario.ProfilePlateau, VUs: 100, Rate: 100, DurationSeconds: 60, WarmupSeconds: 10},
		Code:      &scenario.CodeRepo{Repo: "dono/repo", Branch: "master", BuildWorkflow: "nac-build.yml", Image: "ghcr.io/dono/repo"}}
}

// runCycle leva uma tentativa até o PR e deixa o ciclo rodar.
func runCycle(t *testing.T, sc scenario.Scenario, b *fakeBuilder, envs *fakeEnvs, exps *fakeExperiments) Attempt {
	t.Helper()
	needGit(t)
	sb := &fakeSandbox{results: []sandbox.Result{{Applied: true, Diff: rootDiff, TestExit: 0}}}
	m := setup(t, sb, &fakePublisher{})
	m.Builder, m.Envs, m.Experiments, m.Scenarios, m.CyclePoll = b, envs, exps, fakeScenarios{sc}, time.Millisecond
	m.Create(context.Background(), attempt("a1"))
	if _, err := m.Approve(context.Background(), "a1", "mateus"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		a, _ := m.Get(context.Background(), "a1")
		if a.Cycle != nil && (a.Cycle.Stage == StageDone || a.Cycle.Stage == StageFailed || a.Cycle.Stage == StageSkipped) && a.Cycle.CleanedUp {
			return a
		}
	}
	a, _ := m.Get(context.Background(), "a1")
	t.Fatalf("o ciclo não terminou: %+v", a.Cycle)
	return a
}

// O critério de pronto da Fase 3: da aprovação ao PR com o resultado estatístico, e nada sobra
// no cluster.
func TestCycleEndsWithResultOnPRAndNothingLeft(t *testing.T) {
	b, envs, exps := &fakeBuilder{}, &fakeEnvs{}, &fakeExperiments{outcome: stats.Improved}
	a := runCycle(t, k8sScenario(), b, envs, exps)
	c := a.Cycle
	if a.Status != PROpen || c.Stage != StageDone || c.Outcome != stats.Improved || !c.Commented || !c.CleanedUp || c.ExperimentID != "exp1" {
		t.Fatalf("ciclo: %+v", c)
	}
	// As duas versões saem do mesmo pipeline: A é o commit testado, B é o branch do PR.
	if len(b.builds) != 2 || b.builds[0].Ref != commit || b.builds[0].Tag != "nac-base-"+commit[:12] || b.builds[1].Ref != "nac/fix-a1" || b.builds[1].Tag != "nac-fix-a1" || b.builds[0].DispatchRef != "master" {
		t.Fatalf("builds: %+v", b.builds)
	}
	if len(envs.created) != 1 || envs.created[0] != "a1 ghcr.io/dono/repo:nac-base-"+commit[:12]+" ghcr.io/dono/repo:nac-fix-a1" {
		t.Fatalf("ambiente: %v", envs.created)
	}
	if len(envs.deleted) != 1 || envs.deleted[0] != "a1" {
		t.Fatalf("as cópias têm que ser apagadas: %v", envs.deleted)
	}
	// O experimento roda contra as cópias, nunca contra o alvo de verdade, com a métrica da correção.
	if exps.sc.Target.Namespace != "efemero" || exps.spec.B == nil || exps.spec.B.Deployment != "nac-a1-b" || exps.spec.Metric.Name != experiment.MetricP95 {
		t.Fatalf("experimento: alvo A %+v, B %+v, métrica %+v", exps.sc.Target, exps.spec.B, exps.spec.Metric)
	}
	if len(b.comments) != 1 {
		t.Fatalf("um comentário no PR: %d", len(b.comments))
	}
	for _, want := range []string{"Melhorou", "sem IA", "800.00", "240.00", "-70.0%", "5 de A e 5 de B", "de 800 ms para menos de 300 ms", "Piso de ruído", "#/experimentos/exp1", "foram apagadas do cluster"} {
		if !strings.Contains(b.comments[0], want) {
			t.Errorf("comentário sem %q:\n%s", want, b.comments[0])
		}
	}
}

// Qualquer desfecho ruim: o PR fica sabendo que o ganho não foi medido, e nada sobra no cluster.
func TestCycleFailuresStillCleanUpAndTellThePR(t *testing.T) {
	for name, c := range map[string]struct {
		b       *fakeBuilder
		envs    *fakeEnvs
		exps    *fakeExperiments
		wants   string
		created int
		deleted int
	}{
		"build de B falha":        {&fakeBuilder{failTag: "nac-fix"}, &fakeEnvs{}, &fakeExperiments{}, "build da imagem", 0, 0},
		"cópias não sobem":        {&fakeBuilder{}, &fakeEnvs{err: errors.New("ImagePullBackOff")}, &fakeExperiments{}, "não subiram", 1, 1},
		"experimento sem decisão": {&fakeBuilder{}, &fakeEnvs{}, &fakeExperiments{status: experiment.Failed}, "não chegou a uma decisão", 1, 1},
	} {
		a := runCycle(t, k8sScenario(), c.b, c.envs, c.exps)
		if a.Status != PROpen || a.Cycle.Stage != StageFailed || !strings.Contains(a.Cycle.Note, c.wants) || !a.Cycle.CleanedUp {
			t.Errorf("%s: %+v", name, a.Cycle)
		}
		if len(c.envs.created) != c.created || len(c.envs.deleted) != c.deleted {
			t.Errorf("%s: criadas %d, apagadas %d", name, len(c.envs.created), len(c.envs.deleted))
		}
		if len(c.b.comments) != 1 || !strings.Contains(c.b.comments[0], "não foi medido") {
			t.Errorf("%s: o PR tem que saber que o ganho não foi medido: %v", name, c.b.comments)
		}
	}
}

// Sem o build configurado, ou com um cenário que não serve para A/B, o ciclo para no PR e diz por quê.
func TestCycleSkips(t *testing.T) {
	noBuild := k8sScenario()
	noBuild.Code.BuildWorkflow = ""
	closed := k8sScenario()
	closed.Profile.Rate = 0
	docker := k8sScenario()
	docker.Target = scenario.Target{Container: "pix", BaseURL: "http://pix:8080"}
	for name, c := range map[string]struct {
		sc    scenario.Scenario
		wants string
	}{"sem build": {noBuild, "construir a imagem"}, "carga em VUs": {closed, "taxa de chegada"}, "alvo no Docker": {docker, "Kubernetes"}} {
		b, envs := &fakeBuilder{}, &fakeEnvs{}
		a := runCycle(t, c.sc, b, envs, &fakeExperiments{})
		if a.Status != PROpen || a.Cycle.Stage != StageSkipped || !strings.Contains(a.Cycle.Note, c.wants) {
			t.Errorf("%s: %+v", name, a.Cycle)
		}
		if len(b.builds)+len(envs.created)+len(b.comments) != 0 {
			t.Errorf("%s: nada deveria ter rodado", name)
		}
	}
}

func TestMetricFor(t *testing.T) {
	load, chaos := k8sScenario(), k8sScenario()
	chaos.Mode = scenario.ModeChaos
	for text, want := range map[string]string{
		"p95 de GET /pix-keys": "p95", "p99": "p99", "taxa de erro": "error_rate", "latência média": "avg", "": "p95", "throughput": "p95", "nota de resiliência": "p95",
	} {
		if got := MetricFor(text, load).Name; got != want {
			t.Errorf("MetricFor(%q) = %s, esperado %s", text, got, want)
		}
	}
	if got := MetricFor("p95", chaos).Name; got != "score" {
		t.Errorf("cenário só de caos mede a nota: %s", got)
	}
}
