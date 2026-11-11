// Package chaos injeta e reverte falhas no container alvo.
//
// No MVP, as falhas de rede e de recursos rodam num container auxiliar
// ("injetor") que compartilha o namespace de rede do alvo
// (--network container:<alvo>, com NET_ADMIN) e tem tc e stress-ng instalados.
package chaos

import (
	"context"
	"fmt"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Injector aplica uma falha e devolve a função que a reverte.
// Toda falha aplicada TEM que ser revertida, inclusive em caso de abort.
type Injector interface {
	Apply(ctx context.Context, targetRef string, f scenario.Fault) (revert func(context.Context) error, err error)
}

// Commands traduz uma falha nos comandos executados dentro do injetor.
// Retorna o comando de aplicar e o de reverter.
func Commands(f scenario.Fault) (apply []string, revert []string, err error) {
	p := func(k, def string) string {
		if v, ok := f.Params[k]; ok && v != "" {
			return v
		}
		return def
	}
	switch f.Type {
	case scenario.FaultLatency:
		return []string{"tc", "qdisc", "add", "dev", "eth0", "root", "netem", "delay", p("delayMs", "200") + "ms", p("jitterMs", "0") + "ms"},
			[]string{"tc", "qdisc", "del", "dev", "eth0", "root"}, nil
	case scenario.FaultPacketLoss:
		return []string{"tc", "qdisc", "add", "dev", "eth0", "root", "netem", "loss", p("percent", "10") + "%"},
			[]string{"tc", "qdisc", "del", "dev", "eth0", "root"}, nil
	case scenario.FaultCPUStress:
		return []string{"stress-ng", "--cpu", p("workers", "1"), "--cpu-load", p("load", "80"), "--timeout", fmt.Sprintf("%ds", int(f.Duration.Seconds()))},
			[]string{"pkill", "stress-ng"}, nil
	case scenario.FaultMemStress:
		return []string{"stress-ng", "--vm", "1", "--vm-bytes", p("megabytes", "256") + "M", "--timeout", fmt.Sprintf("%ds", int(f.Duration.Seconds()))},
			[]string{"pkill", "stress-ng"}, nil
	case scenario.FaultKillRestart:
		// Feito direto pela Docker API, não por comando no injetor.
		return nil, nil, nil
	default:
		return nil, nil, fmt.Errorf("falha desconhecida: %s", f.Type)
	}
}
