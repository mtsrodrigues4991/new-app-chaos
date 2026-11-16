package experiment

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/runner"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func openScenario() scenario.Scenario {
	return scenario.Scenario{
		ID: "sc", Name: "demo", Mode: scenario.ModeLoad,
		Target:    scenario.Target{Container: "demo-api", BaseURL: "http://demo-api:8080"},
		Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/items", Weight: 1}, {Method: "GET", Path: "/compute", Weight: 1}},
		Profile:   scenario.Profile{Type: scenario.ProfilePlateau, VUs: 50, Rate: 200, DurationSeconds: 30, WarmupSeconds: 10},
	}
}

// fakeRuns faz o papel do runner: cada Start grava uma execução já terminada, com o p95 que
// p95Of devolver para o alvo. Guarda a ordem dos alvos chamados.
type fakeRuns struct {
	st    *store.Store
	p95Of func(target string, n int) float64
	// Opcionais.
	dropped func(target string, n int) int
	image   func(target string, n int) string
	busy    int // quantas vezes responder "ocupado" antes de aceitar

	mu      sync.Mutex
	order   []string
	aborted []string
}

func (f *fakeRuns) Start(ctx context.Context, sc scenario.Scenario) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy > 0 {
		f.busy--
		return store.Run{}, runner.ErrBusy
	}
	tgt := sc.Target.Name()
	n := 0
	for _, t := range f.order {
		if t == tgt {
			n++
		}
	}
	f.order = append(f.order, tgt)
	now := time.Now().UTC()
	run := store.Run{ID: runner.NewID(), ScenarioID: sc.ID, Scenario: sc, Status: store.RunPending, CreatedAt: now}
	if err := f.st.CreateRun(ctx, run); err != nil {
		return run, err
	}
	p95 := f.p95Of(tgt, n)
	seg := load.Segment{Seconds: 20, Requests: 4000, RPS: 200, P50Ms: p95 / 2, P95Ms: p95, P99Ms: p95 * 2, AvgMs: p95 / 2}
	sum := load.Summary{OpenModel: true, TargetRate: 200, WarmupSeconds: 10, Overall: seg, Steady: seg,
		Endpoints: []load.EndpointSummary{{Method: "GET", Path: "/items", Segment: seg}, {Method: "GET", Path: "/compute", Segment: seg}}}
	if f.dropped != nil {
		sum.DroppedIterations = f.dropped(tgt, n)
	}
	img := "sha256:" + tgt
	if f.image != nil {
		img = f.image(tgt, n)
	}
	_ = f.st.SaveSummary(ctx, run.ID, sum)
	_ = f.st.SaveVersions(ctx, run.ID, []store.Version{{Role: store.RoleTarget, Name: tgt, Images: []store.Image{{Image: tgt, ID: img}}}})
	_ = f.st.SetRunStatus(ctx, run.ID, store.RunCompleted, "", &now, &now)
	return run, nil
}

func (f *fakeRuns) Abort(ctx context.Context, id string) error {
	f.mu.Lock()
	f.aborted = append(f.aborted, id)
	f.mu.Unlock()
	return nil
}

func setup(t *testing.T, f *fakeRuns) *Manager {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f.st = st
	m := NewManager(f, st)
	m.Poll = time.Millisecond
	m.second = time.Microsecond
	return m
}

func wait(t *testing.T, m *Manager, id string) Experiment {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		e, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Status.Done() {
			return e
		}
	}
	t.Fatal("o experimento não terminou")
	return Experiment{}
}

func start(t *testing.T, m *Manager, spec Spec) Experiment {
	t.Helper()
	spec.ScenarioID = "sc"
	e, err := m.Start(context.Background(), spec, openScenario())
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, m, e.ID)
}

// ruído pequeno e fixo em torno de um valor.
func around(base float64, n int) float64 {
	return base * (1 + []float64{0.004, -0.006, 0.002, -0.003, 0.005, -0.001, 0.003, -0.004, 0.001, -0.002}[n%10])
}

var slowB = &scenario.Target{Container: "demo-api-b", BaseURL: "http://demo-api-b:8080"}

// O critério de pronto da fase: A/A dá "sem diferença" e mede o piso de ruído; uma versão B
// com 20 ms a mais dá "piorou".
func TestAAThenSlowerB(t *testing.T) {
	f := &fakeRuns{p95Of: func(target string, n int) float64 {
		if target == "demo-api-b" {
			return around(25, n)
		}
		return around(5, n)
	}}
	m := setup(t, f)

	aa := start(t, m, Spec{})
	if aa.Status != Completed || aa.Result == nil || aa.Result.Outcome != stats.NoDifference {
		t.Fatalf("A/A deveria dar sem diferença: %+v %+v", aa.Status, aa.Result)
	}
	if aa.Result.NoisePct == nil || *aa.Result.NoisePct > 5 {
		t.Fatalf("o A/A mede o piso de ruído: %+v", aa.Result.NoisePct)
	}
	// Decidiu no mínimo: 5 execuções por lado, intercaladas e alternando quem vai primeiro.
	if len(aa.Runs) != 10 {
		t.Fatalf("esperava parar em 10 execuções, foram %d", len(aa.Runs))
	}
	if arms := armsOf(aa.Runs); arms != "ABBAABBAAB" {
		t.Fatalf("ordem das execuções: %s", arms)
	}

	ab := start(t, m, Spec{B: slowB})
	if ab.Result == nil || ab.Result.Outcome != stats.Worsened || ab.Result.Primary.Diff < 19 {
		t.Fatalf("B com +20 ms deveria dar piorou: %+v", ab.Result)
	}
	// O A/B usa o piso de ruído do A/A do mesmo cenário e métrica.
	if ab.Result.NoisePct == nil || *ab.Result.NoisePct != *aa.Result.NoisePct {
		t.Fatalf("piso de ruído do A/A anterior: %v", ab.Result.NoisePct)
	}
	if ab.Runs[0].Version == ab.Runs[1].Version {
		t.Fatalf("cada lado registra a sua versão: %+v", ab.Runs[:2])
	}
}

func armsOf(runs []ArmRun) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Arm)
	}
	return b.String()
}

// Execução com requisições descartadas não entra na conta, e o experimento roda mais pares
// até ter o mínimo de execuções válidas.
func TestInvalidRunsAreReplaced(t *testing.T) {
	f := &fakeRuns{
		p95Of:   func(target string, n int) float64 { return around(5, n) },
		dropped: func(target string, n int) int { return map[bool]int{true: 37}[n == 1] }, // a 2ª execução (a primeira de B)
	}
	m := setup(t, f)
	e := start(t, m, Spec{})
	if len(e.Runs) != 12 || e.Result.Primary.NA != 6 || e.Result.Primary.NB != 5 {
		t.Fatalf("esperava 12 execuções (1 inválida), 6 válidas de A e 5 de B: %d %+v", len(e.Runs), e.Result.Primary)
	}
	if r := e.Runs[1]; r.Valid || !strings.Contains(r.Invalid, "37 requisições descartadas") {
		t.Fatalf("a execução com descarte deveria ser inválida: %+v", r)
	}
}

// Ruído maior que o efeito: roda até o teto e termina "inconclusivo", que é um resultado.
func TestNoisyStopsAtCeilingInconclusive(t *testing.T) {
	noisy := []float64{80, 120, 95, 140, 100, 70, 130, 90, 150, 85}
	f := &fakeRuns{p95Of: func(target string, n int) float64 {
		if target == "demo-api-b" {
			return noisy[(n+3)%10]
		}
		return noisy[n%10]
	}}
	m := setup(t, f)
	e := start(t, m, Spec{B: slowB, MaxRuns: 7})
	if e.Status != Completed || e.Result.Outcome != stats.Inconclusive || len(e.Runs) != 14 {
		t.Fatalf("esperava inconclusivo no teto de 7 por lado: %s %+v (%d execuções)", e.Status, e.Result.Outcome, len(e.Runs))
	}
	if !containsNote(e.Result.Notes, "Não há teste A/A") {
		t.Fatalf("sem A/A anterior o resultado tem que avisar: %v", e.Result.Notes)
	}
}

// Se a imagem de um lado muda no meio, as execuções não medem a mesma coisa: inconclusivo.
func TestVersionChangeInvalidatesDecision(t *testing.T) {
	f := &fakeRuns{
		p95Of: func(target string, n int) float64 {
			if target == "demo-api-b" {
				return around(25, n)
			}
			return around(5, n)
		},
		image: func(target string, n int) string {
			if target == "demo-api-b" && n >= 3 {
				return "sha256:outra"
			}
			return "sha256:" + target
		},
	}
	m := setup(t, f)
	e := start(t, m, Spec{B: slowB, MaxRuns: 5})
	if e.Result.Outcome != stats.Inconclusive || !containsNote(e.Result.Notes, "A versão de B mudou") {
		t.Fatalf("troca de versão no meio: %+v", e.Result)
	}
}

// Um ganho menor que duas vezes o piso de ruído do A/A não é afirmado.
func TestGainBelowNoiseFloorIsNotClaimed(t *testing.T) {
	e := Experiment{Spec: Spec{B: slowB, Metric: Metric{Name: MetricP95}, MarginPct: 5}}
	for i := 0; i < 5; i++ {
		e.Runs = append(e.Runs,
			ArmRun{Arm: ArmA, Valid: true, Values: map[string]float64{MetricP95: around(100, i)}},
			ArmRun{Arm: ArmB, Valid: true, Values: map[string]float64{MetricP95: around(92, i)}})
	}
	quiet, loud := 1.0, 6.0
	if r := Decide(e, &quiet); r.Outcome != stats.Improved {
		t.Fatalf("8%% de ganho com piso de 1%%: %+v", r)
	}
	if r := Decide(e, &loud); r.Outcome != stats.Inconclusive || !containsNote(r.Notes, "piso de ruído") {
		t.Fatalf("8%% de ganho com piso de 6%% não dá para afirmar: %+v", r)
	}
}

// Baixar o p95 dobrando os erros não é ganho: a guarda manda.
func TestGuardOverridesImprovement(t *testing.T) {
	e := Experiment{Spec: Spec{B: slowB, Metric: Metric{Name: MetricP95}, MarginPct: 5}}
	for i := 0; i < 5; i++ {
		e.Runs = append(e.Runs,
			ArmRun{Arm: ArmA, Valid: true, Values: map[string]float64{MetricP95: around(100, i), MetricErrorRate: 0}},
			ArmRun{Arm: ArmB, Valid: true, Values: map[string]float64{MetricP95: around(60, i), MetricErrorRate: around(4, i)}})
	}
	noise := 1.0
	r := Decide(e, &noise)
	if r.Primary.Outcome != stats.Improved || r.Outcome != stats.Worsened || len(r.Guards) != 1 {
		t.Fatalf("p95 melhor com erro pior tem que dar piorou: %+v", r)
	}
}

func containsNote(notes []string, s string) bool {
	return strings.Contains(strings.Join(notes, "\n"), s)
}

func TestSpecValidate(t *testing.T) {
	sc := openScenario()
	ok := Spec{ScenarioID: "sc"}
	ok.Normalize()
	if err := ok.Validate(sc); err != nil {
		t.Fatalf("esperava válido: %v", err)
	}
	closed := sc
	closed.Profile.Rate = 0
	one := 1
	nine := 9
	for name, c := range map[string]struct {
		spec Spec
		sc   scenario.Scenario
	}{
		"carga em VUs":         {ok, closed},
		"menos de 5 execuções": {Spec{MinRuns: 3, MaxRuns: 10, MarginPct: 5, Metric: Metric{Name: MetricP95}}, sc},
		"nota sem falhas":      {Spec{MinRuns: 5, MaxRuns: 10, MarginPct: 5, Metric: Metric{Name: MetricScore}}, sc},
		"endpoint inexistente": {Spec{MinRuns: 5, MaxRuns: 10, MarginPct: 5, Metric: Metric{Name: MetricP95, Endpoint: &nine}}, sc},
		"B igual a A":          {Spec{MinRuns: 5, MaxRuns: 10, MarginPct: 5, Metric: Metric{Name: MetricP95}, B: &sc.Target}, sc},
		"B noutra plataforma":  {Spec{MinRuns: 5, MaxRuns: 10, MarginPct: 5, Metric: Metric{Name: MetricP95}, B: &scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "demo", Deployment: "b", BaseURL: "http://b.demo:8080"}}, sc},
	} {
		if err := c.spec.Validate(c.sc); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
	withEndpoint := ok
	withEndpoint.Metric.Endpoint = &one
	if err := withEndpoint.Validate(sc); err != nil {
		t.Errorf("métrica por endpoint: %v", err)
	}
}

// Só um experimento por vez; uma execução avulsa ocupando o runner só atrasa.
func TestBusyAndRecover(t *testing.T) {
	f := &fakeRuns{p95Of: func(string, int) float64 { return 5 }, busy: 3}
	m := setup(t, f)
	e := start(t, m, Spec{MaxRuns: 5})
	if e.Status != Completed || len(e.Runs) != 10 {
		t.Fatalf("runner ocupado no começo só atrasa: %s %d", e.Status, len(e.Runs))
	}
	// Um experimento que ficou "rodando" no banco (backend caiu) é fechado na subida.
	stuck := Experiment{ID: "preso", Spec: Spec{ScenarioID: "sc"}, Status: Running, CreatedAt: time.Now()}
	m.save(&stuck)
	m.Recover(context.Background())
	if got, _ := m.Get(context.Background(), "preso"); got.Status != Failed || !strings.Contains(got.Error, "interrompido") {
		t.Fatalf("experimento interrompido: %+v", got)
	}
}
