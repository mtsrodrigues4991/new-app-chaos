// Package runner orquestra uma execução: sobe o k6 e/ou a sonda, injeta as falhas da
// linha do tempo, coleta métricas do k6 e do container alvo, agrega em janelas de 1 s,
// salva no banco e transmite ao vivo.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaos"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/obs"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/probe"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// RoleK6 é o label dos containers do k6 criados pelo app.
const RoleK6 = "k6"

// DefaultK6Image é a imagem do k6 usada nas execuções.
const DefaultK6Image = "grafana/k6:0.54.0"

// Event é o que o frontend recebe ao vivo.
type Event struct {
	Type    string            `json:"type"` // "windows", "status" ou "fault"
	Windows []load.Window     `json:"windows,omitempty"`
	Fault   *store.FaultEvent `json:"fault,omitempty"`
	Status  store.RunStatus   `json:"status,omitempty"`
	Error   string            `json:"error,omitempty"`
}

type active struct {
	mu      sync.Mutex
	k6      K6
	aborted bool
	cancel  context.CancelFunc // interrompe a execução (usado no abort)
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
	Docker  *dockerx.Client // nil se o Docker não estiver acessível
	Kube    *kube.Client    // nil fora de um cluster Kubernetes
	Store   *store.Store
	Chaos   *chaos.Engine
	K6Image string
	// Obs lê a telemetria da aplicação na stack de observabilidade (nil = desligada; só vale
	// para alvos no Kubernetes).
	Obs *obs.Client
	// ObsSettle é quanto esperar, no fim, as últimas métricas e traces chegarem à stack.
	ObsSettle time.Duration

	mu   sync.Mutex
	runs map[string]*active
}

func NewManager(d *dockerx.Client, k *kube.Client, s *store.Store) *Manager {
	return &Manager{Docker: d, Kube: k, Store: s, Chaos: &chaos.Engine{Docker: d}, K6Image: DefaultK6Image, ObsSettle: 10 * time.Second, runs: map[string]*active{}}
}

// platform devolve a plataforma do alvo do cenário, ou erro se ela não está acessível.
func (m *Manager) platform(p scenario.Platform) (Platform, error) {
	switch p {
	case scenario.PlatformDocker:
		if m.Docker == nil {
			return nil, errors.New("Docker não acessível pelo backend (no Kubernetes, use um alvo Deployment)")
		}
		return &dockerPlatform{docker: m.Docker, chaos: m.Chaos}, nil
	case scenario.PlatformKubernetes:
		if m.Kube == nil {
			return nil, errors.New("o backend não está rodando num cluster Kubernetes (suba com make kind-up)")
		}
		return newK8sPlatform(m.Kube), nil
	}
	return nil, fmt.Errorf("plataforma desconhecida: %q", p)
}

// Recover limpa o que sobrou de uma execução interrompida (backend reiniciado no meio).
func (m *Manager) Recover(ctx context.Context) {
	// Primeiro desfaz falhas que possam ter ficado aplicadas no alvo.
	runs, err := m.Store.InterruptedRuns(ctx)
	if err != nil {
		log.Printf("recover: %v", err)
	}
	for _, p := range []scenario.Platform{scenario.PlatformDocker, scenario.PlatformKubernetes} {
		if pl, err := m.platform(p); err == nil {
			pl.Recover(ctx, runs)
		}
	}
	if n, err := m.Store.FailInterrupted(ctx); err != nil {
		log.Printf("recover: %v", err)
	} else if n > 0 {
		log.Printf("recover: %d execução(ões) interrompida(s) marcadas como falha", n)
	}
}

// ErrBusy indica que já existe uma execução em andamento.
var ErrBusy = errors.New("já existe uma execução em andamento; aborte-a ou espere terminar")

// Start valida o cenário, registra a execução e a dispara em segundo plano.
func (m *Manager) Start(ctx context.Context, sc scenario.Scenario) (store.Run, error) {
	if err := sc.Validate(); err != nil {
		return store.Run{}, err
	}
	pl, err := m.platform(sc.Target.EffectivePlatform())
	if err != nil {
		return store.Run{}, err
	}
	target, err := pl.Resolve(ctx, sc)
	if err != nil {
		return store.Run{}, err
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
	go m.execute(run, target, a)
	return run, nil
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

// Abort interrompe a execução: para as métricas, reverte a falha ativa e mata o k6.
// O fluxo normal de término registra "aborted".
func (m *Manager) Abort(ctx context.Context, runID string) error {
	m.mu.Lock()
	a, ok := m.runs[runID]
	m.mu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	a.mu.Lock()
	a.aborted = true
	k6, cancel := a.k6, a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if k6 == nil {
		return nil // sem k6 (só caos) ou ainda baixando a imagem; o execute vê a flag
	}
	return k6.Kill(ctx) // já parado não é erro
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

func (m *Manager) execute(run store.Run, target Target, a *active) {
	defer m.finish(run.ID)
	ctx := context.Background()
	sc := run.Scenario
	fail := func(err error) {
		now := time.Now().UTC()
		log.Printf("run %s: %v", run.ID, err)
		m.setStatus(run.ID, a, store.RunFailed, err.Error(), nil, &now)
	}
	isAborted := func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.aborted
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	a.mu.Lock()
	a.cancel = cancelRun
	a.mu.Unlock()

	var script string
	if sc.HasLoad() {
		if err := target.PrepareLoad(ctx, m.K6Image); err != nil {
			fail(err)
			return
		}
		var err error
		if script, err = load.Script(sc); err != nil {
			fail(err)
			return
		}
	}
	var probeURL string
	if sc.EffectiveMode() == scenario.ModeChaos {
		var err error
		if probeURL, err = target.ProbeURL(ctx, sc); err != nil {
			fail(err)
			return
		}
	}
	if isAborted() {
		now := time.Now().UTC()
		m.setStatus(run.ID, a, store.RunAborted, "", nil, &now)
		return
	}

	// --- k6 (modos carga e carga + caos) ---
	var k6 K6
	exited := make(chan k6Exit, 1)
	if sc.HasLoad() {
		var err error
		k6, err = target.StartK6(ctx, run.ID, m.K6Image, script)
		if k6 != nil {
			defer func() {
				if err := k6.Remove(context.Background()); err != nil {
					log.Printf("run %s: removendo k6: %v", run.ID, err)
				}
			}()
		}
		if err != nil {
			fail(err)
			return
		}
		a.mu.Lock()
		a.k6 = k6
		a.mu.Unlock()
		if isAborted() {
			cancelRun()
			_ = k6.Kill(ctx)
		}
		go func() {
			code, err := k6.Wait(ctx)
			exited <- k6Exit{code, err}
		}()
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

	bgCtx, stopBG := context.WithCancel(runCtx)
	defer stopBG()
	var bg sync.WaitGroup
	bg.Add(2)
	go func() { // CPU e memória do alvo; reconecta se o alvo cair (kill/restart)
		defer bg.Done()
		for bgCtx.Err() == nil {
			err := target.Stats(bgCtx, func(s Sample) {
				now := time.Now()
				aggMu.Lock()
				if !s.NoUsage {
					agg.AddStats(now, s.CPU, s.MemMB)
				}
				if s.Total > 0 || sc.Target.EffectivePlatform() == scenario.PlatformKubernetes {
					agg.AddPods(now, s.Ready, s.Total)
				}
				aggMu.Unlock()
			})
			if err != nil && bgCtx.Err() == nil {
				log.Printf("run %s: stats do alvo: %v", run.ID, err)
			}
			select {
			case <-bgCtx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	// Janelas fecham com folga: o resumo do k6 chega ~1 s depois do período e a sonda
	// pode levar até probe.Timeout para responder.
	lag := int64(2)
	if probeURL != "" {
		lag = int64(probe.Timeout/time.Second) + 1
	}
	go func() {
		defer bg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case now := <-t.C:
				aggMu.Lock()
				ws := agg.Flush(agg.Elapsed(now) - lag)
				aggMu.Unlock()
				emit(ws)
			}
		}
	}()
	collector, _ := target.(Collector)
	if collector != nil {
		bg.Add(1)
		go func() { // logs de tempos em tempos (um pod que cai leva o log junto)
			defer bg.Done()
			t := time.NewTicker(SnapshotEvery)
			defer t.Stop()
			for {
				select {
				case <-bgCtx.Done():
					return
				case <-t.C:
					sctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
					collector.Snapshot(sctx, started)
					cancel()
				}
			}
		}()
	}
	if probeURL != "" {
		bg.Add(1)
		go func() {
			defer bg.Done()
			probe.Run(bgCtx, probeURL, time.Second, func(at time.Time, p load.Probe) {
				aggMu.Lock()
				agg.AddProbe(at, p)
				aggMu.Unlock()
			})
		}()
	}

	// --- falhas da linha do tempo (modos só caos e carga + caos) ---
	faultCtx, stopFaults := context.WithCancel(runCtx)
	defer stopFaults()
	faultDone := make(chan error, 1)
	var faultErr error
	if sc.HasChaos() {
		go func() {
			err := m.runFaults(faultCtx, run.ID, a, target, sc.SortedFaults(), started)
			if err != nil && !isAborted() {
				// Falha que não aplicou ou não reverteu: o resultado não vale; encerra o teste.
				log.Printf("run %s: %v", run.ID, err)
				cancelRun()
				if k6 != nil {
					_ = k6.Kill(context.Background())
				}
			}
			faultDone <- err
		}()
	} else {
		faultDone <- nil
	}

	// --- espera o fim ---
	var exitRes *k6Exit
	var streamErr error
	if sc.HasLoad() {
		exitRes, streamErr = m.waitK6(ctx, runCtx, run, k6, agg, &aggMu, exited)
	} else {
		select {
		case <-time.After(time.Duration(sc.Profile.DurationSeconds) * time.Second):
		case <-runCtx.Done():
		}
	}

	stopFaults() // se o k6 terminou antes, a falha ativa é revertida agora
	faultErr = <-faultDone
	stopBG()
	bg.Wait()
	aggMu.Lock()
	rest := agg.Flush(load.MaxSecond)
	aggMu.Unlock()
	emit(rest)

	if collector != nil {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		diag := collector.Diagnostics(dctx, started)
		cancel()
		if m.Obs != nil && sc.Target.EffectivePlatform() == scenario.PlatformKubernetes {
			diag.App = m.appTelemetry(sc, started)
		}
		if err := m.Store.SaveDiagnostics(ctx, run.ID, diag); err != nil {
			log.Printf("run %s: salvando logs e eventos: %v", run.ID, err)
		}
	}

	ended := time.Now().UTC()
	switch {
	case isAborted():
		m.setStatus(run.ID, a, store.RunAborted, "", nil, &ended)
	case faultErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, faultErr.Error(), nil, &ended)
	case exitRes != nil && exitRes.err != nil:
		m.setStatus(run.ID, a, store.RunFailed, "esperando o k6: "+exitRes.err.Error(), nil, &ended)
	case exitRes != nil && exitRes.code != 0:
		tail, _ := k6.LogsTail(ctx, 20)
		m.setStatus(run.ID, a, store.RunFailed, fmt.Sprintf("k6 saiu com código %d: %s", exitRes.code, lastLines(tail, 5)), nil, &ended)
	case streamErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, "lendo as métricas do k6: "+streamErr.Error(), nil, &ended)
	default:
		m.setStatus(run.ID, a, store.RunCompleted, "", nil, &ended)
	}
}

// appTelemetry lê na stack de observabilidade as métricas, traces e profiling do alvo e das
// dependências usadas nas falhas.
func (m *Manager) appTelemetry(sc scenario.Scenario, started time.Time) *store.AppTelemetry {
	time.Sleep(m.ObsSettle) // as métricas chegam a cada 5 s e o profiling a cada 10 s
	svcs := []obs.Service{{Namespace: sc.Target.Namespace, Name: sc.Target.Deployment}}
	seen := map[string]bool{svcs[0].String(): true}
	for _, d := range deps(sc) {
		s := obs.Service{Namespace: d.Namespace, Name: d.Deployment}
		if !seen[s.String()] {
			seen[s.String()] = true
			svcs = append(svcs, s)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.Obs.Collect(ctx, svcs, started, time.Now())
}

type k6Exit struct {
	code int
	err  error
}

// waitK6 lê o dashboard até o k6 anunciar o fim e espera o container sair.
// Se o k6 sair sem anunciar, ou passar muito do previsto, corta.
func (m *Manager) waitK6(ctx, runCtx context.Context, run store.Run, k6 K6, agg *load.Aggregator, aggMu *sync.Mutex, exited chan k6Exit) (*k6Exit, error) {
	streamCtx, stopStream := context.WithCancel(runCtx)
	defer stopStream()
	var exitRes *k6Exit
	streamDone := make(chan error, 1)
	go func() { streamDone <- m.streamDashboard(streamCtx, k6, agg, aggMu) }()
	watchdog := time.NewTimer(time.Duration(run.Scenario.Profile.DurationSeconds)*time.Second + 2*time.Minute)
	defer watchdog.Stop()
	var streamErr error
	select {
	case streamErr = <-streamDone:
	case <-watchdog.C:
		log.Printf("run %s: passou muito do tempo previsto; encerrando", run.ID)
		stopStream()
		streamErr = <-streamDone
		_ = k6.Kill(ctx)
	case e := <-exited:
		exitRes = &e
		select {
		case streamErr = <-streamDone:
		case <-time.After(3 * time.Second):
			stopStream()
			streamErr = <-streamDone
		}
	}
	stopStream()

	// Fim do teste: o k6 sai logo depois que o leitor do dashboard desconecta.
	if exitRes == nil {
		select {
		case e := <-exited:
			exitRes = &e
		case <-time.After(15 * time.Second):
			log.Printf("run %s: k6 não saiu sozinho; matando", run.ID)
			_ = k6.Kill(ctx)
			e := <-exited
			exitRes = &e
		}
	}
	return exitRes, streamErr
}

// runFaults aplica e reverte as falhas na hora marcada, uma por vez. Quando ctx acaba
// (abort ou fim do teste), reverte a falha ativa e marca as restantes como não executadas.
// Uma falha que não pôde ser aplicada fica marcada como failed e a execução segue; devolve
// erro se uma falha não pôde ser revertida.
func (m *Manager) runFaults(ctx context.Context, runID string, a *active, target Target, faults []scenario.Fault, started time.Time) error {
	at := func(sec int) time.Time { return started.Add(time.Duration(sec) * time.Second) }
	since := func() *float64 { v := float64(time.Since(started).Milliseconds()) / 1000; return &v }
	save := func(ev store.FaultEvent) {
		if err := m.Store.SaveFaultEvent(context.Background(), runID, ev); err != nil {
			log.Printf("run %s: salvando falha: %v", runID, err)
		}
		a.broadcast(Event{Type: "fault", Fault: &ev})
	}
	skipRest := func(from int) {
		for _, f := range faults[from:] {
			save(store.FaultEvent{FaultID: f.ID, Type: f.Type, State: store.FaultSkipped})
		}
	}
	for i, f := range faults {
		select {
		case <-ctx.Done():
			skipRest(i)
			return nil
		case <-time.After(time.Until(at(f.StartSeconds))):
		}
		if c, ok := target.(Collector); ok { // antes da falha: um kill leva o log do pod junto
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			c.Snapshot(sctx, started)
			cancel()
		}
		ev := store.FaultEvent{FaultID: f.ID, Type: f.Type}
		// Aplica com contexto próprio: um abort no meio não pode deixar a falha "meio aplicada".
		// Esperar a confirmação não passa do fim do bloco: uma falha que não entra não pode
		// empurrar as próximas para fora do horário.
		wait := min(time.Minute, max(10*time.Second, time.Until(at(f.End()))))
		actx, cancel := context.WithTimeout(context.Background(), wait)
		revert, err := target.ApplyFault(actx, runID, f)
		cancel()
		if err != nil {
			// A falha não entrou (e já saiu do alvo): marca e segue com as próximas. A
			// execução continua válida, só sem essa falha na linha do tempo.
			ev.State, ev.Error = store.FaultFailed, err.Error()
			save(ev)
			log.Printf("run %s: falha %s em %ds não aplicada: %v", runID, f.Type, f.StartSeconds, err)
			continue
		}
		ev.State, ev.AppliedAt = store.FaultActive, since()
		save(ev)

		select {
		case <-ctx.Done():
		case <-time.After(time.Until(at(f.End()))):
		}
		rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err = revert(rctx)
		if err != nil { // uma segunda tentativa resolve falhas momentâneas do Docker
			time.Sleep(time.Second)
			err = revert(rctx)
		}
		cancel()
		ev.RevertedAt = since()
		if err != nil {
			ev.State, ev.Error = store.FaultFailed, "revertendo: "+err.Error()
			save(ev)
			skipRest(i + 1)
			return fmt.Errorf("a falha %s em %ds não pôde ser revertida (confira o alvo): %w", f.Type, f.StartSeconds, err)
		}
		ev.State = store.FaultReverted
		save(ev)
		if ctx.Err() != nil {
			skipRest(i + 1)
			return nil
		}
	}
	return nil
}

const dashboardPort = 5665

// streamDashboard espera o dashboard do k6 subir e lê os resumos até o fim do teste.
func (m *Manager) streamDashboard(ctx context.Context, k6 K6, agg *load.Aggregator, mu *sync.Mutex) error {
	var url string
	// No Kubernetes o pod pode levar mais para subir (agendamento e download da imagem).
	deadline := time.Now().Add(3 * time.Minute)
	for {
		u, err := k6.DashboardURL(ctx)
		if err != nil {
			return err
		}
		if u != "" {
			url = u
			err = load.StreamDashboard(ctx, url, agg.Start(), func(s *load.Snapshot) {
				mu.Lock()
				agg.AddSnapshot(s)
				mu.Unlock()
			})
			if err == nil || ctx.Err() != nil {
				return nil
			}
			var opErr *net.OpError
			if !errors.As(err, &opErr) { // erro de formato, não de conexão: não adianta tentar de novo
				return err
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Now().After(deadline) {
			if url == "" {
				return errors.New("o k6 não ficou pronto em 3 minutos (no Kubernetes: kubectl get pods -l new-app-chaos/role=k6)")
			}
			return fmt.Errorf("dashboard do k6 não respondeu em %s: %v", url, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
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
