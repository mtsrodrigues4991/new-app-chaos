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
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaos"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
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
	k6ID    string
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
	Docker  *dockerx.Client
	Store   *store.Store
	Chaos   *chaos.Engine
	K6Image string

	mu   sync.Mutex
	runs map[string]*active
}

func NewManager(d *dockerx.Client, s *store.Store) *Manager {
	return &Manager{Docker: d, Store: s, Chaos: &chaos.Engine{Docker: d}, K6Image: DefaultK6Image, runs: map[string]*active{}}
}

// Recover limpa o que sobrou de uma execução interrompida (backend reiniciado no meio).
func (m *Manager) Recover(ctx context.Context) {
	// Primeiro desfaz falhas que possam ter ficado aplicadas no alvo.
	if runs, err := m.Store.InterruptedRuns(ctx); err != nil {
		log.Printf("recover: %v", err)
	} else if m.Docker != nil {
		for _, r := range runs {
			if !r.Scenario.HasChaos() || len(r.Scenario.Faults) == 0 {
				continue
			}
			types := map[scenario.FaultType]bool{}
			for _, f := range r.Scenario.Faults {
				types[f.Type] = true
			}
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			if err := m.Chaos.Cleanup(cctx, r.Scenario.Target.Container, types); err != nil {
				log.Printf("recover: revertendo falhas no alvo %s: %v", r.Scenario.Target.Container, err)
			} else {
				log.Printf("recover: falhas revertidas no alvo %s", r.Scenario.Target.Container)
			}
			cancel()
		}
	}
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
	_, _ = m.Docker.RemoveByRole(ctx, chaos.RoleTools)
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
	targetID, networks, running, err := m.Docker.Inspect(ctx, sc.Target.Container)
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
		return store.Run{}, fmt.Errorf("container alvo %q não está numa rede que o app possa usar", sc.Target.Container)
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
	go m.execute(run, targetID, network, a)
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
	id, cancel := a.k6ID, a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if id == "" {
		return nil // sem k6 (só caos) ou ainda baixando a imagem; o execute vê a flag
	}
	return m.Docker.Kill(ctx, id, "SIGKILL") // já parado não é erro
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

func (m *Manager) execute(run store.Run, targetID, network string, a *active) {
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
		if err := m.Docker.EnsureImage(ctx, m.K6Image); err != nil {
			fail(fmt.Errorf("baixando a imagem do k6: %w", err))
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
		if probeURL, err = m.probeURL(ctx, sc, targetID, network); err != nil {
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
	var k6ID, reachNet string
	exited := make(chan k6Exit, 1)
	if sc.HasLoad() {
		var err error
		k6ID, reachNet, err = m.startK6(ctx, run, script, network)
		if k6ID != "" {
			defer func() {
				if err := m.Docker.Remove(context.Background(), k6ID); err != nil {
					log.Printf("run %s: removendo k6: %v", run.ID, err)
				}
			}()
		}
		if err != nil {
			fail(err)
			return
		}
		a.mu.Lock()
		a.k6ID = k6ID
		a.mu.Unlock()
		if isAborted() {
			cancelRun()
			_ = m.Docker.Kill(ctx, k6ID, "SIGKILL")
		}
		go func() {
			code, err := m.Docker.Wait(ctx, k6ID)
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
			err := m.Docker.Stats(bgCtx, targetID, func(cpu, mem float64) {
				aggMu.Lock()
				agg.AddStats(time.Now(), cpu, mem)
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
			err := m.runFaults(faultCtx, run.ID, a, targetID, sc.SortedFaults(), started)
			if err != nil && !isAborted() {
				// Falha que não aplicou ou não reverteu: o resultado não vale; encerra o teste.
				log.Printf("run %s: %v", run.ID, err)
				cancelRun()
				if k6ID != "" {
					_ = m.Docker.Kill(context.Background(), k6ID, "SIGKILL")
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
		exitRes, streamErr = m.waitK6(ctx, runCtx, run, k6ID, reachNet, agg, &aggMu, exited)
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

	ended := time.Now().UTC()
	switch {
	case isAborted():
		m.setStatus(run.ID, a, store.RunAborted, "", nil, &ended)
	case faultErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, faultErr.Error(), nil, &ended)
	case exitRes != nil && exitRes.err != nil:
		m.setStatus(run.ID, a, store.RunFailed, "esperando o k6: "+exitRes.err.Error(), nil, &ended)
	case exitRes != nil && exitRes.code != 0:
		tail, _ := m.Docker.LogsTail(ctx, k6ID, 20)
		m.setStatus(run.ID, a, store.RunFailed, fmt.Sprintf("k6 saiu com código %d: %s", exitRes.code, lastLines(tail, 5)), nil, &ended)
	case streamErr != nil:
		m.setStatus(run.ID, a, store.RunFailed, "lendo as métricas do k6: "+streamErr.Error(), nil, &ended)
	default:
		m.setStatus(run.ID, a, store.RunCompleted, "", nil, &ended)
	}
}

type k6Exit struct {
	code int
	err  error
}

// startK6 cria, liga à rede certa e inicia o container do k6.
// Devolve o id (mesmo em caso de erro, para quem chama remover) e a rede pela qual o backend o alcança.
func (m *Manager) startK6(ctx context.Context, run store.Run, script, network string) (string, string, error) {
	k6ID, err := m.Docker.Create(ctx, dockerx.RunSpec{
		Name:       "new-app-chaos-k6-" + run.ID,
		Image:      m.K6Image,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{`printf '%s' "$K6_SCRIPT" > /tmp/script.js && exec k6 run --quiet --no-summary --no-usage-report /tmp/script.js`},
		Env: []string{
			"K6_SCRIPT=" + script,
			// Dashboard embutido do k6: publica as métricas já agregadas por segundo em :5665/events.
			"K6_WEB_DASHBOARD=true",
			"K6_WEB_DASHBOARD_HOST=0.0.0.0",
			fmt.Sprintf("K6_WEB_DASHBOARD_PORT=%d", dashboardPort),
			"K6_WEB_DASHBOARD_PERIOD=1s",
		},
		Network: network,
		Role:    RoleK6,
		RunID:   run.ID,
	})
	if err != nil {
		return "", "", fmt.Errorf("criando o container do k6: %w", err)
	}

	// O backend precisa alcançar o dashboard do k6. Se ele roda num container, liga o k6
	// também à rede do backend; se roda direto na máquina, usa o IP na rede do alvo.
	reachNet := network
	if self := m.Docker.SelfNetworks(ctx); len(self) > 0 {
		reachNet = ""
		for _, n := range self {
			if n == network {
				reachNet = n
			}
		}
		if reachNet == "" {
			reachNet = pickNetwork(self)
			if err := m.Docker.ConnectNetwork(ctx, reachNet, k6ID); err != nil {
				return k6ID, "", fmt.Errorf("ligando o k6 à rede do backend (%s): %w", reachNet, err)
			}
		}
	}
	if err := m.Docker.Start(ctx, k6ID); err != nil {
		return k6ID, "", fmt.Errorf("iniciando o k6: %w", err)
	}
	return k6ID, reachNet, nil
}

// waitK6 lê o dashboard até o k6 anunciar o fim e espera o container sair.
// Se o k6 sair sem anunciar, ou passar muito do previsto, corta.
func (m *Manager) waitK6(ctx, runCtx context.Context, run store.Run, k6ID, reachNet string, agg *load.Aggregator, aggMu *sync.Mutex, exited chan k6Exit) (*k6Exit, error) {
	streamCtx, stopStream := context.WithCancel(runCtx)
	defer stopStream()
	var exitRes *k6Exit
	streamDone := make(chan error, 1)
	go func() { streamDone <- m.streamDashboard(streamCtx, k6ID, reachNet, agg, aggMu) }()
	watchdog := time.NewTimer(time.Duration(run.Scenario.Profile.DurationSeconds)*time.Second + 2*time.Minute)
	defer watchdog.Stop()
	var streamErr error
	select {
	case streamErr = <-streamDone:
	case <-watchdog.C:
		log.Printf("run %s: passou muito do tempo previsto; encerrando", run.ID)
		stopStream()
		streamErr = <-streamDone
		_ = m.Docker.Kill(ctx, k6ID, "SIGKILL")
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
			_ = m.Docker.Kill(ctx, k6ID, "SIGKILL")
			e := <-exited
			exitRes = &e
		}
	}
	return exitRes, streamErr
}

// runFaults aplica e reverte as falhas na hora marcada, uma por vez. Quando ctx acaba
// (abort ou fim do teste), reverte a falha ativa e marca as restantes como não executadas.
// Devolve erro se uma falha não pôde ser aplicada ou revertida.
func (m *Manager) runFaults(ctx context.Context, runID string, a *active, targetID string, faults []scenario.Fault, started time.Time) error {
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
		ev := store.FaultEvent{FaultID: f.ID, Type: f.Type}
		// Aplica com contexto próprio: um abort no meio não pode deixar a falha "meio aplicada".
		actx, cancel := context.WithTimeout(context.Background(), time.Minute)
		revert, err := m.Chaos.Apply(actx, targetID, f)
		cancel()
		if err != nil {
			ev.State, ev.Error = store.FaultFailed, err.Error()
			save(ev)
			skipRest(i + 1)
			return fmt.Errorf("a falha %s em %ds não pôde ser aplicada: %w", f.Type, f.StartSeconds, err)
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

// probeURL monta a URL da sonda e garante que o backend alcança o alvo: se o backend roda
// num container fora da rede do alvo, ele entra nessa rede; se roda na máquina, usa o IP do alvo.
func (m *Manager) probeURL(ctx context.Context, sc scenario.Scenario, targetID, network string) (string, error) {
	u, err := url.Parse(strings.TrimRight(sc.Target.BaseURL, "/") + sc.ProbePath)
	if err != nil {
		return "", fmt.Errorf("URL da sonda inválida: %w", err)
	}
	if selfID, _ := m.Docker.Self(ctx); selfID != "" {
		for _, n := range m.Docker.SelfNetworks(ctx) {
			if n == network {
				return u.String(), nil
			}
		}
		if err := m.Docker.ConnectNetwork(ctx, network, selfID); err != nil && !strings.Contains(err.Error(), "already exists") {
			return "", fmt.Errorf("ligando o backend à rede do alvo (%s) para a sonda: %w", network, err)
		}
		return u.String(), nil
	}
	ips, err := m.Docker.IPs(ctx, targetID)
	if err != nil {
		return "", err
	}
	if ip := ips[network]; ip != "" {
		if port := u.Port(); port != "" {
			u.Host = net.JoinHostPort(ip, port)
		} else {
			u.Host = ip
		}
	}
	return u.String(), nil
}

const dashboardPort = 5665

// streamDashboard espera o dashboard do k6 subir e lê os resumos até o fim do teste.
func (m *Manager) streamDashboard(ctx context.Context, k6ID, network string, agg *load.Aggregator, mu *sync.Mutex) error {
	var url string
	deadline := time.Now().Add(60 * time.Second)
	for {
		ips, err := m.Docker.IPs(ctx, k6ID)
		if err == nil && ips[network] != "" {
			url = fmt.Sprintf("http://%s:%d/events", ips[network], dashboardPort)
			err = load.StreamDashboard(ctx, url, func(s *load.Snapshot) {
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
