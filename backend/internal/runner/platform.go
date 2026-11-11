package runner

import (
	"context"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaos"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Platform é onde o alvo roda (Docker ou Kubernetes). O fluxo da execução (carga, sonda,
// falhas na linha do tempo, agregação, veredito) é o mesmo; muda só como cada peça é feita.
type Platform interface {
	// Resolve confere que o alvo existe e está de pé e devolve o que a execução precisa.
	Resolve(ctx context.Context, sc scenario.Scenario) (Target, error)
	// Recover desfaz o que execuções interrompidas deixaram (falhas e geradores de carga).
	Recover(ctx context.Context, interrupted []store.Run)
}

// Target é o alvo já resolvido de uma execução.
type Target interface {
	// PrepareLoad deixa o gerador de carga pronto (ex.: baixa a imagem do k6).
	PrepareLoad(ctx context.Context, image string) error
	// StartK6 sobe o k6 com o script. Devolve o k6 mesmo com erro, para quem chama remover.
	StartK6(ctx context.Context, runID, image, script string) (K6, error)
	// Stats chama fn a cada amostra de CPU e memória do alvo, até ctx acabar.
	Stats(ctx context.Context, fn func(Sample)) error
	// ApplyFault aplica a falha e devolve como desfazê-la.
	ApplyFault(ctx context.Context, runID string, f scenario.Fault) (chaos.Revert, error)
	// ProbeURL devolve a URL que a sonda (modo só caos) consulta.
	ProbeURL(ctx context.Context, sc scenario.Scenario) (string, error)
}

// Sample é uma amostra do consumo do alvo.
type Sample struct {
	CPU   float64 // % (100 = 1 núcleo), somando todos os pods no Kubernetes
	MemMB float64
	// Pods prontos e existentes (só no Kubernetes; Total = 0 no Docker).
	Ready, Total int
	// NoUsage indica que só a contagem de pods veio nesta amostra (métricas indisponíveis).
	NoUsage bool
}

// K6 é um k6 em execução.
type K6 interface {
	// DashboardURL devolve a URL do /events do dashboard do k6; "" enquanto ainda não dá para saber.
	DashboardURL(ctx context.Context) (string, error)
	// Wait espera o k6 terminar e devolve o código de saída.
	Wait(ctx context.Context) (int, error)
	Kill(ctx context.Context) error
	Remove(ctx context.Context) error
	LogsTail(ctx context.Context, n int) (string, error)
}
