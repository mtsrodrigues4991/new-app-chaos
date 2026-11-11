// Package chaosmesh traduz as falhas da linha do tempo em recursos do Chaos Mesh
// (chaos-mesh.org/v1alpha1), o motor de caos usado quando o alvo roda no Kubernetes.
//
// O tempo fica com o new-app-chaos, como na linha do tempo do Docker: o recurso é criado no
// início do bloco e apagado no fim (o Chaos Mesh desfaz a falha ao apagar). O campo duration
// do recurso é só uma rede de segurança: se o backend cair, a falha se desfaz sozinha.
package chaosmesh

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Group é o grupo/versão da API do Chaos Mesh.
const Group = "chaos-mesh.org/v1alpha1"

// LabelManaged marca os recursos criados pelo app (para a limpeza no Recover).
const (
	LabelManaged = "app.kubernetes.io/managed-by"
	ManagedBy    = "new-app-chaos"
	LabelRun     = "new-app-chaos/run"
)

// SafetyMarginSeconds é quanto o duration do recurso passa do fim do bloco.
const SafetyMarginSeconds = 30

// Kind descreve um tipo de recurso do Chaos Mesh.
type Kind struct {
	Kind   string // ex.: NetworkChaos
	Plural string // ex.: networkchaos (caminho na API)
}

var (
	NetworkChaos = Kind{"NetworkChaos", "networkchaos"}
	StressChaos  = Kind{"StressChaos", "stresschaos"}
	PodChaos     = Kind{"PodChaos", "podchaos"}
	IOChaos      = Kind{"IOChaos", "iochaos"}
	DNSChaos     = Kind{"DNSChaos", "dnschaos"}
	HTTPChaos    = Kind{"HTTPChaos", "httpchaos"}
)

// Kinds são todos os tipos que o app cria (para a limpeza).
var Kinds = []Kind{NetworkChaos, StressChaos, PodChaos, IOChaos, DNSChaos, HTTPChaos}

// Target é o Deployment atingido.
type Target struct {
	Namespace   string
	MatchLabels map[string]string
}

var invalidName = regexp.MustCompile(`[^a-z0-9-]+`)

// Name monta um nome válido no Kubernetes (minúsculas, números e hífen, até 63).
func Name(parts ...string) string {
	n := invalidName.ReplaceAllString(strings.ToLower(strings.Join(parts, "-")), "-")
	n = strings.Trim(n, "-")
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	return n
}

func (t Target) selector() map[string]any {
	return map[string]any{"namespaces": []string{t.Namespace}, "labelSelectors": t.MatchLabels}
}

// Build monta o recurso do Chaos Mesh para a falha f nos pods de t. Com link (só latência e
// perda), a falha vale apenas para o tráfego de t até os pods de link (uma dependência).
func Build(f scenario.Fault, t Target, link *Target, name, runID string) (Kind, map[string]any, error) {
	if err := f.ValidateParams(); err != nil {
		return Kind{}, nil, err
	}
	if link != nil && !f.Type.IsNetwork() {
		return Kind{}, nil, fmt.Errorf("só latência e perda de pacotes podem ficar restritas ao caminho até uma dependência")
	}
	spec := map[string]any{
		"mode":     f.PodsMode(),
		"selector": t.selector(),
	}
	if link != nil {
		// Do alvo para a dependência: os pacotes que saem do alvo rumo a ela (as consultas).
		spec["target"] = map[string]any{"mode": "all", "selector": link.selector()}
	}
	duration := fmt.Sprintf("%ds", f.DurationSeconds+SafetyMarginSeconds)
	var kind Kind
	switch f.Type {
	case scenario.FaultLatency:
		kind = NetworkChaos
		spec["action"] = "delay"
		spec["direction"] = "to"
		spec["delay"] = map[string]any{
			"latency":     f.Param(scenario.ParamLatency) + "ms",
			"jitter":      f.Param(scenario.ParamJitter) + "ms",
			"correlation": "0",
		}
		spec["duration"] = duration
	case scenario.FaultPacketLoss:
		kind = NetworkChaos
		spec["action"] = "loss"
		spec["direction"] = "to"
		spec["loss"] = map[string]any{"loss": f.Param(scenario.ParamLoss), "correlation": "0"}
		spec["duration"] = duration
	case scenario.FaultCPUStress:
		kind = StressChaos
		spec["stressors"] = map[string]any{"cpu": map[string]any{
			"workers": f.IntParam(scenario.ParamCPUWorkers),
			"load":    f.IntParam(scenario.ParamCPULoad),
		}}
		spec["duration"] = duration
	case scenario.FaultMemStress:
		kind = StressChaos
		spec["stressors"] = map[string]any{"memory": map[string]any{
			"workers": 1,
			"size":    MemSize(f.Param(scenario.ParamMemBytes)),
		}}
		spec["duration"] = duration
	case scenario.FaultKillRestart:
		// No Kubernetes o "kill" mata o(s) pod(s) no início do bloco e o Deployment recria;
		// o bloco é o tempo observado da recuperação.
		kind = PodChaos
		spec["action"] = "pod-kill"
		spec["gracePeriod"] = 0
	case scenario.FaultIOLatency, scenario.FaultIOFault:
		kind = IOChaos
		spec["volumePath"] = f.Param(scenario.ParamVolumePath)
		if p := f.Param(scenario.ParamPath); p != "" {
			spec["path"] = p
		}
		spec["percent"] = f.IntParam(scenario.ParamPercent)
		if f.Type == scenario.FaultIOLatency {
			spec["action"] = "latency"
			spec["delay"] = f.Param(scenario.ParamDelay) + "ms"
		} else {
			spec["action"] = "fault"
			spec["errno"] = f.IntParam(scenario.ParamErrno)
		}
		spec["duration"] = duration
	case scenario.FaultDNSError:
		kind = DNSChaos
		spec["action"] = "error"
		if p := f.Patterns(); len(p) > 0 {
			spec["patterns"] = p
		}
		spec["duration"] = duration
	case scenario.FaultHTTPDelay, scenario.FaultHTTPAbort:
		kind = HTTPChaos
		spec["target"] = "Request"
		spec["port"] = f.IntParam(scenario.ParamPort)
		spec["path"] = f.Param(scenario.ParamPath)
		if m := strings.ToUpper(f.Param(scenario.ParamMethod)); m != "" {
			spec["method"] = m
		}
		if f.Type == scenario.FaultHTTPDelay {
			spec["delay"] = f.Param(scenario.ParamDelay) + "ms"
		} else {
			spec["abort"] = true
		}
		spec["duration"] = duration
	default:
		return Kind{}, nil, fmt.Errorf("tipo de falha sem equivalente no Chaos Mesh: %q", f.Type)
	}
	obj := map[string]any{
		"apiVersion": Group,
		"kind":       kind.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": t.Namespace,
			"labels":    map[string]string{LabelManaged: ManagedBy, LabelRun: runID},
		},
		"spec": spec,
	}
	return kind, obj, nil
}

// MemSize converte o tamanho no formato do stressor (256m, 1g, 512k, 80%) para o do
// Chaos Mesh (256MB, 1GB, 512KB, 80%).
func MemSize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	switch strings.ToLower(v[len(v)-1:]) {
	case "k", "m", "g":
		return v[:len(v)-1] + strings.ToUpper(v[len(v)-1:]) + "B"
	}
	return v
}

// Path é a coleção do tipo no namespace (para criar) ou o objeto (com nome).
func Path(k Kind, ns, name string) string {
	p := fmt.Sprintf("/apis/%s/namespaces/%s/%s", Group, ns, k.Plural)
	if name != "" {
		p += "/" + name
	}
	return p
}

// Condition é uma condição do status de um recurso do Chaos Mesh.
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Status é o pedaço do status que o app lê.
type Status struct {
	Conditions []Condition `json:"conditions"`
	Experiment struct {
		ContainerRecords []struct {
			ID     string `json:"id"`
			Phase  string `json:"phase"`
			Events []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"events"`
		} `json:"containerRecords"`
	} `json:"experiment"`
}

// Injected diz se o Chaos Mesh já aplicou a falha em todos os pods escolhidos. Devolve
// também a última mensagem de erro de injeção, se houver.
func (s Status) Injected() (bool, string) {
	var lastErr string
	for _, r := range s.Experiment.ContainerRecords {
		for _, e := range r.Events {
			if e.Type == "Failed" && e.Message != "" {
				lastErr = e.Message
			}
		}
	}
	for _, c := range s.Conditions {
		if c.Type == "AllInjected" && c.Status == "True" {
			return true, lastErr
		}
	}
	// Versões que não publicam a condição: todos os registros na fase Injected.
	recs := s.Experiment.ContainerRecords
	if len(recs) == 0 {
		return false, lastErr
	}
	for _, r := range recs {
		if r.Phase != "Injected" {
			return false, lastErr
		}
	}
	return true, lastErr
}
