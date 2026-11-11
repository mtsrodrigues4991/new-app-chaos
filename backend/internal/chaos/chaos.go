// Package chaos injeta e reverte falhas no container alvo (marco 3).
//
// A técnica é a mesma dos cenários do krkn, adaptada para Docker sem Kubernetes:
//   - Rede (latência, perda): um container auxiliar entra no namespace de rede do alvo
//     (NetworkMode container:<alvo>, com NET_ADMIN) e aplica tc netem nas interfaces dele.
//     Vale para o tráfego que SAI do alvo, ou seja, as respostas da API.
//   - CPU e memória: o binário estático cmd/stressor é copiado para dentro do alvo e
//     executado via docker exec, para a carga contar no limite do próprio container.
//   - Kill/restart: derruba o container no início do bloco e o sobe de novo no fim.
//
// Toda falha aplicada é revertida: no fim do bloco, ao abortar, e no Recover do backend.
package chaos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// RoleTools é o label dos containers auxiliares de rede (para limpeza).
const RoleTools = "injector"

// stressorDir e stressorName: onde o stressor fica dentro do alvo.
const (
	stressorDir  = "/tmp"
	stressorName = ".new-app-chaos-stressor"
)

// Engine aplica falhas em containers Docker.
type Engine struct {
	Docker *dockerx.Client
	// ToolsImages são referências de uma imagem com sh, ip e tc (a do próprio backend, por
	// padrão), tentadas em ordem até uma existir no Docker.
	ToolsImages []string
	// StressorPath é o binário do stressor no sistema de arquivos do backend.
	StressorPath string
}

// Revert desfaz uma falha. Deve ser seguro chamar mais de uma vez.
type Revert func(context.Context) error

// Apply aplica a falha no container alvo (id) e devolve como revertê-la.
func (e *Engine) Apply(ctx context.Context, target string, f scenario.Fault) (Revert, error) {
	if err := f.ValidateParams(); err != nil {
		return nil, err
	}
	switch f.Type {
	case scenario.FaultLatency, scenario.FaultPacketLoss:
		if err := e.tc(ctx, target, NetemScript(f)); err != nil {
			return nil, err
		}
		return func(ctx context.Context) error { return e.tc(ctx, target, netemClear) }, nil
	case scenario.FaultCPUStress, scenario.FaultMemStress:
		if err := e.stress(ctx, target, StressArgs(f)); err != nil {
			return nil, err
		}
		return func(ctx context.Context) error { return e.stopStress(ctx, target) }, nil
	case scenario.FaultKillRestart:
		if err := e.Docker.StopNow(ctx, target); err != nil {
			return nil, fmt.Errorf("derrubando o container: %w", err)
		}
		return func(ctx context.Context) error {
			if err := e.Docker.Start(ctx, target); err != nil {
				return fmt.Errorf("subindo o container de volta: %w", err)
			}
			return nil
		}, nil
	}
	return nil, fmt.Errorf("tipo de falha desconhecido: %q", f.Type)
}

// Cleanup reverte, sem saber o que estava ativo, tudo que os tipos de falha informados
// podem ter deixado (usado quando o backend reinicia no meio de uma execução).
func (e *Engine) Cleanup(ctx context.Context, target string, types map[scenario.FaultType]bool) error {
	var errs []error
	if types[scenario.FaultKillRestart] {
		if err := e.Docker.Start(ctx, target); err != nil {
			errs = append(errs, err)
		}
	}
	if types[scenario.FaultLatency] || types[scenario.FaultPacketLoss] {
		if err := e.tc(ctx, target, netemClear); err != nil {
			errs = append(errs, err)
		}
	}
	if types[scenario.FaultCPUStress] || types[scenario.FaultMemStress] {
		if err := e.stopStress(ctx, target); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// netemClear remove o netem de todas as interfaces (sem erro se não havia nada).
const netemClear = `for i in $(ip -o link show | awk -F': ' '{print $2}' | cut -d@ -f1 | grep -v '^lo$'); do tc qdisc del dev "$i" root 2>/dev/null; done; true`

// NetemScript monta o script que aplica o netem em todas as interfaces do alvo (menos lo).
func NetemScript(f scenario.Fault) string {
	var rule string
	switch f.Type {
	case scenario.FaultLatency:
		rule = fmt.Sprintf("delay %dms", f.IntParam(scenario.ParamLatency))
		if j := f.IntParam(scenario.ParamJitter); j > 0 {
			rule += fmt.Sprintf(" %dms", j)
		}
	case scenario.FaultPacketLoss:
		rule = fmt.Sprintf("loss %d%%", f.IntParam(scenario.ParamLoss))
	}
	return `set -e; n=0; for i in $(ip -o link show | awk -F': ' '{print $2}' | cut -d@ -f1 | grep -v '^lo$'); do ` +
		`tc qdisc replace dev "$i" root netem ` + rule + `; n=$((n+1)); done; ` +
		`[ "$n" -gt 0 ] || { echo "nenhuma interface de rede encontrada no alvo" >&2; exit 1; }`
}

// StressArgs monta os argumentos do stressor para a falha.
func StressArgs(f scenario.Fault) []string {
	timeout := fmt.Sprintf("%ds", f.DurationSeconds+5) // folga: quem encerra é o revert; o timeout é a rede de segurança
	if f.Type == scenario.FaultCPUStress {
		return []string{"cpu", "-load", f.Param(scenario.ParamCPULoad), "-workers", f.Param(scenario.ParamCPUWorkers), "-timeout", timeout}
	}
	return []string{"mem", "-bytes", f.Param(scenario.ParamMemBytes), "-timeout", timeout}
}

// tc roda o script num container auxiliar que compartilha a rede do alvo.
func (e *Engine) tc(ctx context.Context, target, script string) error {
	if len(e.ToolsImages) == 0 {
		return errors.New("imagem com tc indisponível: rode o backend pelo docker compose ou defina TOOLS_IMAGE")
	}
	var id string
	var err error
	for _, img := range e.ToolsImages {
		id, err = e.Docker.Create(ctx, dockerx.RunSpec{
			Image:      img,
			Entrypoint: []string{"sh", "-c"},
			Cmd:        []string{script},
			Network:    "container:" + target,
			CapAdd:     []string{"NET_ADMIN"},
			Role:       RoleTools,
		})
		if !errors.Is(err, dockerx.ErrNotFound) {
			break
		}
	}
	if errors.Is(err, dockerx.ErrNotFound) {
		return fmt.Errorf("criando o auxiliar de rede: imagem %s não encontrada no Docker (defina TOOLS_IMAGE com o nome da imagem do backend)", strings.Join(e.ToolsImages, " / "))
	}
	if err != nil {
		return fmt.Errorf("criando o auxiliar de rede: %w", err)
	}
	defer func() { _ = e.Docker.Remove(context.Background(), id) }()
	if err := e.Docker.Start(ctx, id); err != nil {
		return fmt.Errorf("iniciando o auxiliar de rede: %w", err)
	}
	code, err := e.Docker.Wait(ctx, id)
	if err != nil {
		return err
	}
	if code != 0 {
		logs, _ := e.Docker.LogsTail(ctx, id, 10)
		if strings.Contains(logs, "qdisc kind is unknown") {
			return errors.New("o kernel desta máquina não tem o módulo netem; carregue com `sudo modprobe sch_netem` e tente de novo")
		}
		return fmt.Errorf("tc saiu com código %d: %s", code, strings.TrimSpace(logs))
	}
	return nil
}

func (e *Engine) stressorBin() string { return stressorDir + "/" + stressorName }

func (e *Engine) stress(ctx context.Context, target string, args []string) error {
	bin, err := os.ReadFile(e.StressorPath)
	if err != nil {
		return fmt.Errorf("stressor não encontrado no backend (%s): %w", e.StressorPath, err)
	}
	if err := e.Docker.PutFile(ctx, target, stressorDir, stressorName, bin, 0o755); err != nil {
		return err
	}
	code, out, err := e.Docker.Exec(ctx, target, append([]string{e.stressorBin()}, args...), true)
	if err != nil {
		return fmt.Errorf("executando o stressor no alvo: %w", err)
	}
	if code > 0 { // -1 = rodando (esperado); 0 = já terminou (tempo curtíssimo)
		return fmt.Errorf("stressor saiu com código %d: %s", code, out)
	}
	return nil
}

func (e *Engine) stopStress(ctx context.Context, target string) error {
	code, out, err := e.Docker.Exec(ctx, target, []string{e.stressorBin(), "stop"}, false)
	if err != nil {
		if strings.Contains(err.Error(), "is not running") {
			return nil // container parado: não há stressor rodando
		}
		return err
	}
	// 126/127: o binário nunca foi copiado para este container, então não há o que parar.
	if code != 0 && code != 126 && code != 127 && !strings.Contains(strings.ToLower(out), "no such file") {
		return fmt.Errorf("parando o stressor (código %d): %s", code, out)
	}
	return nil
}
