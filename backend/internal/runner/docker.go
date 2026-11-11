package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaos"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// dockerPlatform roda tudo no Docker local: o alvo é um container, o k6 é outro container na
// rede dele e as falhas vêm do pacote chaos (tc netem, stressor, stop/start).
type dockerPlatform struct {
	docker *dockerx.Client
	chaos  *chaos.Engine
}

func (p *dockerPlatform) Resolve(ctx context.Context, sc scenario.Scenario) (Target, error) {
	name := sc.Target.Container
	id, networks, running, err := p.docker.Inspect(ctx, name)
	if errors.Is(err, dockerx.ErrNotFound) {
		return nil, fmt.Errorf("container alvo %q não existe", name)
	}
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, fmt.Errorf("container alvo %q não está rodando", name)
	}
	network := pickNetwork(networks)
	if network == "" {
		return nil, fmt.Errorf("container alvo %q não está numa rede que o app possa usar", name)
	}
	return &dockerTarget{p: p, id: id, network: network}, nil
}

func (p *dockerPlatform) Recover(ctx context.Context, interrupted []store.Run) {
	for _, r := range interrupted {
		if r.Scenario.Target.EffectivePlatform() != scenario.PlatformDocker || !r.Scenario.HasChaos() || len(r.Scenario.Faults) == 0 {
			continue
		}
		types := map[scenario.FaultType]bool{}
		for _, f := range r.Scenario.Faults {
			types[f.Type] = true
		}
		name := r.Scenario.Target.Container
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := p.chaos.Cleanup(cctx, name, types); err != nil {
			log.Printf("recover: revertendo falhas no alvo %s: %v", name, err)
		} else {
			log.Printf("recover: falhas revertidas no alvo %s", name)
		}
		cancel()
	}
	if n, err := p.docker.RemoveByRole(ctx, RoleK6); err != nil {
		log.Printf("recover: limpando containers do k6: %v", err)
	} else if n > 0 {
		log.Printf("recover: %d container(es) do k6 removido(s)", n)
	}
	_, _ = p.docker.RemoveByRole(ctx, chaos.RoleTools)
}

func pickNetwork(networks []string) string {
	for _, n := range networks {
		if n != "host" && n != "none" {
			return n
		}
	}
	return ""
}

type dockerTarget struct {
	p       *dockerPlatform
	id      string
	network string
}

func (t *dockerTarget) PrepareLoad(ctx context.Context, image string) error {
	if err := t.p.docker.EnsureImage(ctx, image); err != nil {
		return fmt.Errorf("baixando a imagem do k6: %w", err)
	}
	return nil
}

func (t *dockerTarget) Stats(ctx context.Context, fn func(Sample)) error {
	return t.p.docker.Stats(ctx, t.id, func(cpu, mem float64) { fn(Sample{CPU: cpu, MemMB: mem}) })
}

func (t *dockerTarget) ApplyFault(ctx context.Context, runID string, f scenario.Fault) (chaos.Revert, error) {
	return t.p.chaos.Apply(ctx, t.id, f)
}

// StartK6 cria, liga à rede certa e inicia o container do k6.
func (t *dockerTarget) StartK6(ctx context.Context, runID, image, script string) (K6, error) {
	d := t.p.docker
	id, err := d.Create(ctx, dockerx.RunSpec{
		Name:       "new-app-chaos-k6-" + runID,
		Image:      image,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{k6Command},
		Env:        k6Env(script),
		Network:    t.network,
		Role:       RoleK6,
		RunID:      runID,
	})
	if err != nil {
		return nil, fmt.Errorf("criando o container do k6: %w", err)
	}
	k := &dockerK6{d: d, id: id, reachNet: t.network}

	// O backend precisa alcançar o dashboard do k6. Se ele roda num container, liga o k6
	// também à rede do backend; se roda direto na máquina, usa o IP na rede do alvo.
	if self := d.SelfNetworks(ctx); len(self) > 0 {
		k.reachNet = ""
		for _, n := range self {
			if n == t.network {
				k.reachNet = n
			}
		}
		if k.reachNet == "" {
			k.reachNet = pickNetwork(self)
			if err := d.ConnectNetwork(ctx, k.reachNet, id); err != nil {
				return k, fmt.Errorf("ligando o k6 à rede do backend (%s): %w", k.reachNet, err)
			}
		}
	}
	if err := d.Start(ctx, id); err != nil {
		return k, fmt.Errorf("iniciando o k6: %w", err)
	}
	return k, nil
}

// ProbeURL monta a URL da sonda e garante que o backend alcança o alvo: se o backend roda
// num container fora da rede do alvo, ele entra nessa rede; se roda na máquina, usa o IP do alvo.
func (t *dockerTarget) ProbeURL(ctx context.Context, sc scenario.Scenario) (string, error) {
	d := t.p.docker
	u, err := url.Parse(strings.TrimRight(sc.Target.BaseURL, "/") + sc.ProbePath)
	if err != nil {
		return "", fmt.Errorf("URL da sonda inválida: %w", err)
	}
	if selfID, _ := d.Self(ctx); selfID != "" {
		for _, n := range d.SelfNetworks(ctx) {
			if n == t.network {
				return u.String(), nil
			}
		}
		if err := d.ConnectNetwork(ctx, t.network, selfID); err != nil && !strings.Contains(err.Error(), "already exists") {
			return "", fmt.Errorf("ligando o backend à rede do alvo (%s) para a sonda: %w", t.network, err)
		}
		return u.String(), nil
	}
	ips, err := d.IPs(ctx, t.id)
	if err != nil {
		return "", err
	}
	if ip := ips[t.network]; ip != "" {
		if port := u.Port(); port != "" {
			u.Host = net.JoinHostPort(ip, port)
		} else {
			u.Host = ip
		}
	}
	return u.String(), nil
}

type dockerK6 struct {
	d        *dockerx.Client
	id       string
	reachNet string
}

func (k *dockerK6) DashboardURL(ctx context.Context) (string, error) {
	ips, err := k.d.IPs(ctx, k.id)
	if err != nil || ips[k.reachNet] == "" {
		return "", nil
	}
	return fmt.Sprintf("http://%s:%d/events", ips[k.reachNet], dashboardPort), nil
}

func (k *dockerK6) Wait(ctx context.Context) (int, error) { return k.d.Wait(ctx, k.id) }
func (k *dockerK6) Kill(ctx context.Context) error        { return k.d.Kill(ctx, k.id, "SIGKILL") }
func (k *dockerK6) Remove(ctx context.Context) error      { return k.d.Remove(ctx, k.id) }
func (k *dockerK6) LogsTail(ctx context.Context, n int) (string, error) {
	return k.d.LogsTail(ctx, k.id, n)
}

// k6Command grava o script e roda o k6 (mesma linha no Docker e no Kubernetes).
const k6Command = `printf '%s' "$K6_SCRIPT" > /tmp/script.js && exec k6 run --quiet --no-summary --no-usage-report /tmp/script.js`

// k6Env liga o dashboard embutido do k6, que publica as métricas agregadas por segundo em :5665/events.
func k6Env(script string) []string {
	return []string{
		"K6_SCRIPT=" + script,
		"K6_WEB_DASHBOARD=true",
		"K6_WEB_DASHBOARD_HOST=0.0.0.0",
		fmt.Sprintf("K6_WEB_DASHBOARD_PORT=%d", dashboardPort),
		"K6_WEB_DASHBOARD_PERIOD=1s",
	}
}
