package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/runner"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Runs dispara e aborta execuções (o runner.Manager; trocado por um falso nos testes).
type Runs interface {
	Start(ctx context.Context, sc scenario.Scenario) (store.Run, error)
	Abort(ctx context.Context, runID string) error
}

// ErrBusy indica que já há um experimento em andamento.
var ErrBusy = errors.New("já existe um experimento em andamento; aborte-o ou espere terminar")

// Manager roda um experimento por vez: é a fila que dispara as execuções em sequência.
type Manager struct {
	Runs  Runs
	Store *store.Store
	// Poll é o intervalo das consultas de estado; BusyWait, quanto esperar uma execução avulsa
	// que esteja ocupando o runner antes de desistir.
	Poll     time.Duration
	BusyWait time.Duration

	second time.Duration // um segundo de pausa (encurtado nos testes)

	mu     sync.Mutex
	active string // id do experimento em andamento
	cancel context.CancelFunc
	runID  string // execução em andamento, para o abort
}

func NewManager(r Runs, s *store.Store) *Manager {
	return &Manager{Runs: r, Store: s, Poll: time.Second, BusyWait: 30 * time.Minute, second: time.Second}
}

func (m *Manager) save(e *Experiment) {
	if err := m.Store.SaveExperiment(context.Background(), e.ID, e.ScenarioID, string(e.Status), e.CreatedAt, e); err != nil {
		log.Printf("experimento %s: salvando: %v", e.ID, err)
	}
}

// Get lê um experimento.
func (m *Manager) Get(ctx context.Context, id string) (Experiment, error) {
	var e Experiment
	return e, m.Store.Experiment(ctx, id, &e)
}

// List devolve os experimentos mais recentes (scenarioID vazio = todos).
func (m *Manager) List(ctx context.Context, scenarioID string, limit int) ([]Experiment, error) {
	raw, err := m.Store.Experiments(ctx, scenarioID, "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]Experiment, 0, len(raw))
	for _, r := range raw {
		var e Experiment
		if err := json.Unmarshal(r, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Recover marca como falhos os experimentos que estavam rodando quando o backend parou.
// As execuções deles já são tratadas pelo runner.
func (m *Manager) Recover(ctx context.Context) {
	for _, st := range []string{store.ExperimentPending, store.ExperimentRunning} {
		raw, err := m.Store.Experiments(ctx, "", st, 100)
		if err != nil {
			log.Printf("recover: experimentos: %v", err)
			return
		}
		for _, r := range raw {
			var e Experiment
			if json.Unmarshal(r, &e) != nil {
				continue
			}
			now := time.Now().UTC()
			e.Status, e.Error, e.EndedAt = Failed, "interrompido: o backend reiniciou durante o experimento", &now
			m.save(&e)
			log.Printf("recover: experimento %s marcado como interrompido", e.ID)
		}
	}
}

// Start valida e dispara o experimento em segundo plano. sc é o cenário atual.
func (m *Manager) Start(ctx context.Context, spec Spec, sc scenario.Scenario) (Experiment, error) {
	spec.Normalize()
	if err := sc.Validate(); err != nil {
		return Experiment{}, err
	}
	if err := spec.Validate(sc); err != nil {
		return Experiment{}, err
	}
	if spec.Name == "" {
		spec.Name = sc.Name
	}
	e := Experiment{ID: runner.NewID(), Spec: spec, Scenario: sc, Status: Pending, CreatedAt: time.Now().UTC(), Runs: []ArmRun{}}

	m.mu.Lock()
	if m.active != "" {
		m.mu.Unlock()
		return Experiment{}, ErrBusy
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.active, m.cancel = e.ID, cancel
	m.mu.Unlock()

	if err := m.Store.SaveExperiment(ctx, e.ID, e.ScenarioID, string(e.Status), e.CreatedAt, e); err != nil {
		m.release()
		return Experiment{}, err
	}
	go m.execute(runCtx, e)
	return e, nil
}

func (m *Manager) release() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.active, m.cancel, m.runID = "", nil, ""
	m.mu.Unlock()
}

// Abort interrompe o experimento e a execução em andamento dele.
func (m *Manager) Abort(ctx context.Context, id string) error {
	m.mu.Lock()
	if m.active != id {
		m.mu.Unlock()
		return store.ErrNotFound
	}
	cancel, runID := m.cancel, m.runID
	m.mu.Unlock()
	cancel()
	if runID != "" {
		if err := m.Runs.Abort(ctx, runID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (m *Manager) execute(ctx context.Context, e Experiment) {
	defer m.release()
	e.Status = Running
	m.save(&e)
	finish := func(st Status, msg string) {
		now := time.Now().UTC()
		e.Status, e.Error, e.EndedAt = st, msg, &now
		if len(e.Runs) > 0 {
			res := Decide(e, m.noise(e))
			e.Result = &res
		}
		m.save(&e)
	}

	// Pares intercalados, alternando quem vai primeiro (A B, B A, A B...): uma deriva do
	// ambiente ao longo do tempo pesa igual nos dois lados.
	for pair := 0; pair < e.MaxRuns; pair++ {
		order := []string{ArmA, ArmB}
		if pair%2 == 1 {
			order = []string{ArmB, ArmA}
		}
		for _, arm := range order {
			r, err := m.runOne(ctx, e, arm)
			if ctx.Err() != nil {
				finish(Aborted, "")
				return
			}
			if err != nil {
				finish(Failed, err.Error())
				return
			}
			e.Runs = append(e.Runs, r)
			m.save(&e)
			select {
			case <-ctx.Done():
				finish(Aborted, "")
				return
			case <-time.After(time.Duration(e.CooldownSeconds) * m.second):
			}
		}
		// A partir do mínimo de execuções válidas dos dois lados, para assim que a decisão
		// deixa de ser "inconclusivo".
		if na, nb := len(values(e.Runs, ArmA, e.Metric.Name)), len(values(e.Runs, ArmB, e.Metric.Name)); na >= e.MinRuns && nb >= e.MinRuns {
			if Decide(e, m.noise(e)).Outcome != stats.Inconclusive {
				break
			}
		}
		// Três pares seguidos sem nenhuma execução válida: insistir só gasta tempo.
		if pair == 2 && len(values(e.Runs, ArmA, e.Metric.Name))+len(values(e.Runs, ArmB, e.Metric.Name)) == 0 {
			finish(Failed, "nenhuma das seis primeiras execuções valeu para a comparação: "+e.Runs[len(e.Runs)-1].Invalid)
			return
		}
	}
	finish(Completed, "")
}

// noise devolve o piso de ruído do teste A/A mais recente do mesmo cenário e métrica.
func (m *Manager) noise(e Experiment) *float64 {
	if e.AA() {
		return nil
	}
	list, err := m.List(context.Background(), e.ScenarioID, 50)
	if err != nil {
		return nil
	}
	for _, o := range list { // do mais recente para o mais antigo
		if o.ID == e.ID || !o.AA() || o.Status != Completed || o.Result == nil || o.Result.NoisePct == nil {
			continue
		}
		if o.Metric.Name != e.Metric.Name || !sameEndpoint(o.Metric.Endpoint, e.Metric.Endpoint) {
			continue
		}
		return o.Result.NoisePct
	}
	return nil
}

func sameEndpoint(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// runOne dispara uma execução de um lado, espera terminar e mede. Se o runner estiver ocupado
// com uma execução avulsa, espera a vez em vez de falhar.
func (m *Manager) runOne(ctx context.Context, e Experiment, arm string) (ArmRun, error) {
	sc := e.scenarioFor(arm)
	var run store.Run
	deadline := time.Now().Add(m.BusyWait)
	for {
		var err error
		run, err = m.Runs.Start(ctx, sc)
		if err == nil {
			break
		}
		if !errors.Is(err, runner.ErrBusy) {
			// O alvo não existe, não está pronto ou o cenário não vale mais: o experimento não segue.
			return ArmRun{}, fmt.Errorf("execução de %s não iniciou: %w", arm, err)
		}
		if time.Now().After(deadline) {
			return ArmRun{}, errors.New("outra execução ocupou o app por tempo demais; o experimento desistiu de esperar")
		}
		select {
		case <-ctx.Done():
			return ArmRun{}, ctx.Err()
		case <-time.After(2 * m.Poll):
		}
	}
	m.mu.Lock()
	m.runID = run.ID
	m.mu.Unlock()
	if ctx.Err() != nil { // o abort chegou entre o início e o registro da execução
		_ = m.Runs.Abort(context.Background(), run.ID)
	}
	if err := m.Store.TagRun(context.Background(), run.ID, store.RunTag{ExperimentID: e.ID, Arm: arm}); err != nil {
		log.Printf("experimento %s: marcando a execução %s: %v", e.ID, run.ID, err)
	}

	bg := context.Background() // a leitura do resultado não depende do abort
	for {
		got, err := m.Store.GetRun(bg, run.ID)
		if err != nil {
			return ArmRun{}, err
		}
		if got.Status != store.RunPending && got.Status != store.RunRunning {
			run = got
			break
		}
		select {
		case <-ctx.Done(): // o Abort já pediu ao runner para parar; espera ele registrar o fim
		case <-time.After(m.Poll):
			continue
		}
		time.Sleep(m.Poll)
	}
	m.mu.Lock()
	m.runID = ""
	m.mu.Unlock()

	d := RunData{Run: run}
	var err error
	if d.Windows, err = m.Store.Windows(bg, run.ID); err != nil {
		return ArmRun{}, err
	}
	if d.Events, err = m.Store.FaultEvents(bg, run.ID); err != nil {
		return ArmRun{}, err
	}
	if d.Summary, err = m.Store.Summary(bg, run.ID); err != nil {
		return ArmRun{}, err
	}
	if d.Versions, err = m.Store.Versions(bg, run.ID); err != nil {
		return ArmRun{}, err
	}
	return Measure(arm, e.Metric, d), nil
}
