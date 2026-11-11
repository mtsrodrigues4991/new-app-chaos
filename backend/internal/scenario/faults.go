package scenario

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Parâmetros das falhas. Os nomes seguem os cenários do krkn, para quem já usou se sentir em casa.
const (
	ParamLatency    = "latency"             // ms de atraso (netem delay)
	ParamJitter     = "jitter"              // ms de variação do atraso
	ParamLoss       = "loss"                // % de pacotes perdidos
	ParamCPULoad    = "cpu-load-percentage" // % de carga em cada núcleo estressado
	ParamCPUWorkers = "cpu-workers"         // quantos núcleos estressar
	ParamMemBytes   = "memory-vm-bytes"     // ex.: 256m, 1g ou 80%
	// ParamPods vale só no Kubernetes: "one" atinge um pod do Deployment, "all" atinge todos.
	ParamPods = "pods"

	// Falhas só do Kubernetes (nomes próximos aos do Chaos Mesh).
	ParamDelay      = "delay"       // ms de atraso (IO e HTTP)
	ParamPercent    = "percent"     // % das operações de IO atingidas
	ParamErrno      = "errno"       // erro devolvido nas operações de IO (5 = EIO)
	ParamVolumePath = "volume-path" // ponto de montagem do volume no container (IO)
	ParamPath       = "path"        // arquivos (IO) ou caminho da URL (HTTP), aceita *
	ParamPatterns   = "patterns"    // domínios que falham no DNS, separados por vírgula (vazio = todos)
	ParamPort       = "port"        // porta HTTP dos pods (HTTP)
	ParamMethod     = "method"      // método HTTP atingido (vazio = todos)
)

// Valores de ParamPods.
const (
	PodsOne = "one"
	PodsAll = "all"
)

// PodsMode devolve a quantos pods a falha se aplica no Kubernetes. Padrão: kill em um pod
// (o Deployment recria e as outras réplicas seguram o tráfego); as demais em todos.
func (f Fault) PodsMode() string {
	switch v := strings.TrimSpace(f.Params[ParamPods]); v {
	case PodsOne, PodsAll:
		return v
	}
	if f.Type == FaultKillRestart {
		return PodsOne
	}
	return PodsAll
}

// FaultDefaults são os valores usados quando um parâmetro não é informado.
var FaultDefaults = map[FaultType]map[string]string{
	FaultLatency:     {ParamLatency: "200", ParamJitter: "0"},
	FaultPacketLoss:  {ParamLoss: "10"},
	FaultCPUStress:   {ParamCPULoad: "80", ParamCPUWorkers: "1"},
	FaultMemStress:   {ParamMemBytes: "256m"},
	FaultKillRestart: {},
	FaultIOLatency:   {ParamDelay: "100", ParamPercent: "100"},
	FaultIOFault:     {ParamErrno: "5", ParamPercent: "50"},
	FaultDNSError:    {},
	FaultHTTPDelay:   {ParamDelay: "500", ParamPort: "8080", ParamPath: "*"},
	FaultHTTPAbort:   {ParamPort: "8080", ParamPath: "*"},
}

// Patterns devolve os domínios do DNS, sem espaços e vazios.
func (f Fault) Patterns() []string {
	var out []string
	for _, p := range strings.Split(f.Param(ParamPatterns), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Param devolve o valor do parâmetro ou o padrão do tipo de falha.
func (f Fault) Param(k string) string {
	if v := strings.TrimSpace(f.Params[k]); v != "" {
		return v
	}
	return FaultDefaults[f.Type][k]
}

// IntParam devolve o parâmetro como inteiro (já validado por Validate).
func (f Fault) IntParam(k string) int {
	n, _ := strconv.Atoi(f.Param(k))
	return n
}

// End é o segundo em que a falha é revertida.
func (f Fault) End() int { return f.StartSeconds + f.DurationSeconds }

var memBytesRe = regexp.MustCompile(`^(\d+)([kmgKMG%])?$`)

func intIn(f Fault, k string, min, max int) error {
	n, err := strconv.Atoi(f.Param(k))
	if err != nil || n < min || n > max {
		return fmt.Errorf("%s deve ser um número entre %d e %d", k, min, max)
	}
	return nil
}

// ValidateParams confere os parâmetros de uma falha.
func (f Fault) ValidateParams() error {
	if v := strings.TrimSpace(f.Params[ParamPods]); v != "" && v != PodsOne && v != PodsAll {
		return fmt.Errorf("%s deve ser %q ou %q", ParamPods, PodsOne, PodsAll)
	}
	switch f.Type {
	case FaultLatency:
		if err := intIn(f, ParamLatency, 1, 60000); err != nil {
			return err
		}
		return intIn(f, ParamJitter, 0, 60000)
	case FaultPacketLoss:
		return intIn(f, ParamLoss, 1, 100)
	case FaultCPUStress:
		if err := intIn(f, ParamCPULoad, 1, 100); err != nil {
			return err
		}
		return intIn(f, ParamCPUWorkers, 1, 64)
	case FaultMemStress:
		m := memBytesRe.FindStringSubmatch(f.Param(ParamMemBytes))
		if m == nil {
			return fmt.Errorf("%s deve ser um tamanho (ex.: 256m, 1g) ou percentual (ex.: 80%%)", ParamMemBytes)
		}
		if n, _ := strconv.Atoi(m[1]); n < 1 || (m[2] == "%" && n > 100) {
			return fmt.Errorf("%s fora do intervalo", ParamMemBytes)
		}
		return nil
	case FaultKillRestart, FaultDNSError:
		return nil
	case FaultIOLatency, FaultIOFault:
		if !strings.HasPrefix(f.Param(ParamVolumePath), "/") {
			return fmt.Errorf("%s: informe o ponto de montagem do volume no container (ex.: /var/lib/postgresql/data)", ParamVolumePath)
		}
		if err := intIn(f, ParamPercent, 1, 100); err != nil {
			return err
		}
		if f.Type == FaultIOLatency {
			return intIn(f, ParamDelay, 1, 60000)
		}
		return intIn(f, ParamErrno, 1, 255)
	case FaultHTTPDelay, FaultHTTPAbort:
		if err := intIn(f, ParamPort, 1, 65535); err != nil {
			return err
		}
		if !strings.HasPrefix(f.Param(ParamPath), "/") && f.Param(ParamPath) != "*" {
			return fmt.Errorf("%s deve começar com / ou ser *", ParamPath)
		}
		if m := strings.ToUpper(f.Param(ParamMethod)); m != "" && !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}, m) {
			return fmt.Errorf("%s inválido: %q", ParamMethod, m)
		}
		if f.Type == FaultHTTPDelay {
			return intIn(f, ParamDelay, 1, 60000)
		}
		return nil
	default:
		return fmt.Errorf("tipo de falha desconhecido: %q", f.Type)
	}
}

// validateFaults confere posição, parâmetros e sobreposição (neste marco, uma falha por vez).
func validateFaults(faults []Fault, total int) []string {
	var errs []string
	sorted := append([]Fault(nil), faults...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartSeconds < sorted[j].StartSeconds })
	for i, f := range sorted {
		name := fmt.Sprintf("falha %s em %ds", f.Type, f.StartSeconds)
		if f.StartSeconds < 0 || f.DurationSeconds < 1 {
			errs = append(errs, name+": início deve ser ≥ 0 e duração ≥ 1 s")
		}
		if f.End() > total {
			errs = append(errs, fmt.Sprintf("%s: termina em %ds, depois do fim do teste (%ds)", name, f.End(), total))
		}
		if err := f.ValidateParams(); err != nil {
			errs = append(errs, name+": "+err.Error())
		}
		if i > 0 && f.StartSeconds < sorted[i-1].End() {
			errs = append(errs, fmt.Sprintf("%s se sobrepõe à falha anterior (uma falha por vez neste marco)", name))
		}
	}
	return errs
}

// SortedFaults devolve as falhas em ordem de início.
func (s Scenario) SortedFaults() []Fault {
	out := append([]Fault(nil), s.Faults...)
	sort.Slice(out, func(i, j int) bool { return out[i].StartSeconds < out[j].StartSeconds })
	return out
}
