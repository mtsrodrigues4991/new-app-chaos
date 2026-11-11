// Package runner orquestra uma execução: sobe o k6, coleta métricas do k6 e do
// container alvo, agrega em janelas de 1 s, salva no banco e transmite ao vivo.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// RoleK6 é o label dos containers do k6 criados pelo app.
const RoleK6 = "k6"

// DefaultK6Image é a imagem do k6 usada nas execuções.
const DefaultK6Image = "grafana/k6:0.54.0"

// Event é o que o frontend recebe ao vivo.
type Event struct {
	Type    string          `json:"type"` // "windows" ou "status"
	Windows []load.Window   `json:"windows,omitempty"`
	Status  store.RunStatus `json:"status,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type active struct {
	mu      sync.Mutex
	k6ID    string
	aborted bool
	subs    map[chan Event]struct{}
}

func (a *active) broadcast(ev Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- ev:
		default: // assinante lento: descarta em vez de travar a execução
		}
	}
}

type Manager struct {
	Docker  *dockerx.Client
	Store   *store.Store
	K6Image string

	mu   sync.Mutex
	runs map[string]*active
}

func NewManager(d *dockerx.Client, s *store.Store) *Manager {
	return &Manager{Docker: d, Store: s, K6Image: DefaultK6Image, runs: map[string]*active{}}
}

// Recover limpa o que sobrou de uma execução interrompida (backend reiniciado no meio).
func (m *Manager) Recover(ctx context.Context) {
	if n, err := m.Store.FailInterrupted(ctx); err != nil {
		log.Printf("recover: %v", err)
	} else if n > 0 {
		log.Printf("recover: %d execução(ões) interrompida(s) marcadas como falha", n)
	}
	if m.Docker == nil {
		return
	}
	if n, err := m.Docker.RemoveByRole(ctx, RoleK6); err != nil {
		log.Printf("recover: limpando containers do k6: %v", err)
	} else if n > 0 {
		log.Printf("recover: %d container(es) do k6 removido(s)", n)
	}
}

// ErrBusy indica que já existe uma execução em andamento.
var ErrBusy = errors.New("já existe uma execução em andamento; aborte-a ou espere terminar")

// Start valida o cenário, registra a execução e a dispara em segundo plano.
func (m *Manager) Start(ctx context.Context, sc scenario.Scenario) (store.Run, error) {
	if m.Docker == nil {
		return store.Run{}, errors.New("Docker não acessível pelo backend")
	}
	if err := sc.Validate(); err != nil {
		return store.Run{}, err
	}
	_, networks, running, err := m.Docker.Inspect(ctx, sc.Target.Container)
	if errors.Is(err, dockerx.ErrNotFound) {
		return store.Run{}, fmt.Errorf("container alvo %q não existe", sc.Target.Container)
	}
	if err != nil {
		return store.Run{}, err
	}
	if !running {
		return store.Run{}, fmt.Errorf("container alvo %q não está rodando", sc.Target.Container)
	}
	network := pickNetwork(networks)
	if network == "" {
		return store.Run{}, fmt.Errorf("container alvo %q não está numa rede que o k6 possa usar", sc.Target.Container)
	}

	m.mu.Lock()
	// No MVP, uma execução por vez: o gerador de carga e o alvo dividem a mesma máquina.
	if len(m.runs) > 0 {
		m.mu.Unlock()
		return store.Run{}, ErrBusy
	}
	run := store.Run{ID: NewID(), ScenarioID: sc.ID, ScenarioName: sc.Name, Scenario: sc, Status: store.RunPending, CreatedAt: time.Now().UTC()}
	a := &active{subs: map[chan Event]struct{}{}}
	m.runs[run.ID] = a
	m.mu.Unlock()

	if err := m.Store.CreateRun(ctx, run); err != nil {
		m.finish(run.ID)
		return store.Run{}, err
	}
	go m.execute(run, network, a)
	return run, nil
}

func pickNetwork(networks []string) string {
	for _, n := range networks {
		if n != "host" && n != "none" {
			return n
		}
	}
	return ""
}

// Subscribe devolve um canal com os eventos ao vivo da execução, ou ok=false se ela não está ativa.
func (m *Manager) Subscribe(runID string) (ch chan Event, unsubscribe func(), ok bool) {
	m.mu.Lock()
	a, found := m.runs[runID]
	m.mu.Unlock()
	if !found {
		return nil, nil, false
	}
	ch = make(chan Event, 64)
	a.mu.Lock()
	a.subs[ch] = struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		if _, ok := a.subs[ch]; ok {
			delete(a.subs, ch)
			close(ch)
		}
		a.mu.Unlock()
	}, true
}

// Abort interrompe a execução: mata o k6, e o fluxo normal de término registra "aborted".
func (m *Manager) Abort(ctx context.Context, runID string) error {
	m.mu.Lock()
	a, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	a.mu.Lock()
	a.aborted = true
	id := a.k6ID
	a.mu.Unlock()
	if id == "" {
		return nil // ainda baixando a imagem; o execute vê a flag antes de subir o k6
	}
	return m.Docker.Kill(ctx, id, "SIGKILL")
}

func (m *Manager) finish(runID string) {
	m.mu.Lock()
	a := m.runs[runID]
	delete(m.runs, runID)
	m.mu.Unlock()
	if a == nil {
		return
	}
	a.mu.Lock()
	for ch := range a.subs {
		close(ch)
	}
	a.subs = map[chan Event]struct{}{}
	a.mu.Unlock()
}

func (m *Manager) setStatus(runID string, a *active, st store.RunStatus, msg string, started, ended *time.Time) {
	if err := m.Store.SetRunStatus(context.Background(), runID, st, msg, started, ended); err != nil {
		log.Printf("run %s: salvando status: %v", runID, err)
	}
	a.broadcast(Event{Type: "status", Status: st, Error: msg})
}

func (m *Manager) execute(run store.Run, network string, a *active) {
	defer m.finish(run.ID)
	ctx := context.Background()
	fail := func(err error) {
		now := time.Now().UTC()
		log.Printf("run %s: %v", run.ID, err)
		m.setStatus(run.ID, a, store.RunFailed, err.Error(), nil, &now)
	}

	if err := m.Docker.EnsureImage(ctx, m.K6Image); err != nil {
		fail(fmt.Errorf("baixando a imagem do k6: %w", err))
		return
	}
	script, err := load.Script(run.Scenario)
	if err != nil {
		fail(err)
		return
	}

	a.mu.Lock()
	if a.aborted {
		a.mu.Unlock()
		now := time.Now().UTC()
		m.setStatus(run.ID, a, store.RunAborted, "", nil, &now)
		return
	}
	a.mu.Unlock()

	k6ID, err := m.Docker.CreateAndStart(ctx, dockerx.RunSpec{
		Name:       "new-app-chaos-k6-" + run.ID,
		Image:      m.K6Image,
		Entrypoint: []string{"sh", "-c"},
		Cmd: []string{`printf '%s' "$K6_SCRIPT" > /tmp/script.js && exec k6 run --quiet --no-summary --no-usage-report ` +
			`--out json=/dev/stdout /tmp/script.js`},
		Env:     []string{"K6_SCRIPT=" + script},
		Network: network,
		Role:    RoleK6,
		RunID:   run.ID,
	})
	if err != nil {
		fail(fmt.Errorf("subindo o container do k6: %w", err))
		return
	}
	defer func() {
		if err := m.Docker.Remove(context.Background(), k6ID); err != nil {
			log.Printf("run %s: removendo k6: %v", run.ID, err)
		}
	}()
	a.mu.Lock()
	a.k6ID = k6ID
	abortedEarly := a.aborted
	a.mu.Unlock()
	if abortedEarly {
		_ = m.Docker.Kill(ctx, k6ID, "SIGKILL")
	}

	started := time.Now().UTC()
	m.setStatus(run.ID, a, store.RunRunning, "", &started, nil)

	var aggMu sync.Mutex
	agg := load.NewAggregator(started)
	emit := func(ws []load.Window) {
		if len(ws) == 0 {
			return
		}
		if err := m.Store.AddWindows(ctx, run.ID, ws); err != nil {
			log.Printf("run %s: salvando métricas: %v", run.ID, err)
		}
		a.broadcast(Event{Type: "windows", Windows: ws})
	}

	bgCtx, stopBG := context.WithCancel(ctx)
	var bg sync.WaitGroup
	bg.Add(2)
	go func() { // CPU e memória do alvo
		defer bg.Done()
		err := m.Docker.Stats(bgCtx, run.Scenario.Target.Container, func(cpu, mem float64) {
			aggMu.Lock()
			agg.AddStats(time.Now(), cpu, mem)
			aggMu.Unlock()
		})
		if err != nil {
			log.Printf("run %s: stats do alvo: %v", run.ID, err)
		}
	}()
	go func() { // fecha uma janela por segundo, com 1 s de folga para pontos atrasados
		defer bg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case now := <-t.C:
				aggMu.Lock()
				ws := agg.Flush(agg.Elapsed(now) - 1)
				aggMu.Unlock()
				emit(ws)
			}
		}
	}()

	stderrTail, logErr := m.Docker.FollowStdout(ctx, k6ID, func(line []byte) {
		aggMu.Lock()
		agg.AddK6Line(line)
		aggMu.Unlock()
	})
	code, waitErr := m.Docker.Wait(ctx, k6ID)
	stopBG()
	bg.Wait()
	aggMu.Lock()
	rest := agg.Flush(load.MaxSecond)
	aggMu.Unlock()
	emit(rest)

	ended := time.Now().UTC()
	a.mu.Lock()
	aborted := a.aborted
	a.mu.Unlock()
	switch {
	case aborted:
		m.setStatus(run.ID, a, store.RunAborted, "", nil, &ended)
	case logErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, "lendo a saída do k6: "+logErr.Error(), nil, &ended)
	case waitErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, "esperando o k6: "+waitErr.Error(), nil, &ended)
	case code != 0:
		m.setStatus(run.ID, a, store.RunFailed, fmt.Sprintf("k6 saiu com código %d: %s", code, lastLines(stderrTail, 5)), nil, &ended)
	default:
		m.setStatus(run.ID, a, store.RunCompleted, "", nil, &ended)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// NewID gera um id aleatório curto.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
