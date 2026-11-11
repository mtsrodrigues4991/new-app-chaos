// Package scenario define o modelo de um cenário de teste: alvo, carga, falhas e SLOs.
package scenario

import (
	"errors"
	"fmt"
	"strings"
)

// Mode define o que roda numa execução.
type Mode string

const (
	ModeLoad  Mode = "load"  // só carga
	ModeChaos Mode = "chaos" // só caos (com tráfego leve de sondagem)
	ModeBoth  Mode = "both"  // carga + caos na mesma linha do tempo
)

// Target é a aplicação cuja API será testada. No MVP, um container Docker.
type Target struct {
	Container string `json:"container"` // nome do container no Docker
	BaseURL   string `json:"baseUrl"`   // ex.: http://demo-api:8080 (visto de dentro da rede do Docker)
}

// Endpoint é uma requisição que o k6 vai disparar.
type Endpoint struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Weight  int               `json:"weight"` // peso relativo entre endpoints
}

// ProfileType são os perfis de carga prontos do editor por formulário.
type ProfileType string

const (
	ProfileRamp    ProfileType = "ramp"    // sobe de 0 até VUs ao longo da duração
	ProfilePlateau ProfileType = "plateau" // sobe rápido, mantém, desce
	ProfileSpike   ProfileType = "spike"   // carga base com um pico no meio
)

// Profile é o perfil de carga escolhido no formulário.
type Profile struct {
	Type            ProfileType `json:"type"`
	VUs             int         `json:"vus"`             // usuários virtuais no pico
	DurationSeconds int         `json:"durationSeconds"` // duração total
}

// Stage é um trecho do perfil de carga (equivale a um stage do k6).
type Stage struct {
	DurationSeconds int `json:"durationSeconds"`
	Target          int `json:"target"` // usuários virtuais ao fim do trecho
}

// FaultType são as falhas suportadas no MVP.
type FaultType string

const (
	FaultLatency     FaultType = "latency"
	FaultPacketLoss  FaultType = "packet_loss"
	FaultCPUStress   FaultType = "cpu_stress"
	FaultMemStress   FaultType = "memory_stress"
	FaultKillRestart FaultType = "kill_restart"
)

// Fault é uma falha posicionada na linha do tempo (marco 3).
// Os nomes dos parâmetros seguem os do krkn (ver faults.go).
type Fault struct {
	ID              string            `json:"id"`
	Type            FaultType         `json:"type"`
	StartSeconds    int               `json:"startSeconds"`    // a partir do início da execução
	DurationSeconds int               `json:"durationSeconds"` // depois disso a falha é revertida
	Params          map[string]string `json:"params"`          // ex.: {"latency":"200"}
}

// SLO são os critérios de aprovação do veredito. Cada segundo da execução é "dentro" ou "fora"
// dos SLOs; a nota de resiliência sai daí (ver internal/verdict).
type SLO struct {
	P95Ms           float64 `json:"p95Ms"`           // p95 da carga abaixo disso (modos com carga)
	ErrorRatePct    float64 `json:"errorRatePct"`    // taxa de erro da carga abaixo disso, em %
	AvailabilityPct float64 `json:"availabilityPct"` // disponibilidade da sonda, em % (modo só caos)
}

// CodeRepo aponta o código da aplicação no GitHub.
type CodeRepo struct {
	Repo   string `json:"repo"`           // dono/repo
	Branch string `json:"branch"`         // padrão: main
	Path   string `json:"path,omitempty"` // subpasta da aplicação (monorepo)
}

// DefaultSLO são os valores sugeridos ao criar um cenário.
var DefaultSLO = SLO{P95Ms: 500, ErrorRatePct: 1, AvailabilityPct: 99}

// DefaultRequestTimeout é o tempo máximo de cada requisição do k6 quando o cenário não define.
const DefaultRequestTimeout = 10

// Scenario junta tudo que define um teste.
type Scenario struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Target    Target     `json:"target"`
	Mode      Mode       `json:"mode"`
	Endpoints []Endpoint `json:"endpoints"`
	Profile   Profile    `json:"profile"`
	Faults    []Fault    `json:"faults,omitempty"`
	// ProbePath é o endpoint consultado a cada 1 s no modo só caos (ex.: /health).
	ProbePath string `json:"probePath,omitempty"`
	// SLO ausente (cenários antigos) usa DefaultSLO.
	SLO *SLO `json:"slo,omitempty"`
	// Code é o repositório da aplicação alvo, lido pela IA na análise e no chat (opcional).
	Code *CodeRepo `json:"code,omitempty"`
	// RequestTimeoutSeconds limita cada requisição da carga; a que estoura conta como erro.
	RequestTimeoutSeconds int `json:"requestTimeoutSeconds,omitempty"`
}

// EffectiveSLO devolve os SLOs do cenário ou os sugeridos.
func (s Scenario) EffectiveSLO() SLO {
	if s.SLO == nil {
		return DefaultSLO
	}
	return *s.SLO
}

// RequestTimeout devolve o timeout por requisição em segundos.
func (s Scenario) RequestTimeout() int {
	if s.RequestTimeoutSeconds <= 0 {
		return DefaultRequestTimeout
	}
	return s.RequestTimeoutSeconds
}

// EffectiveMode trata cenários antigos (sem modo) como só carga.
func (s Scenario) EffectiveMode() Mode {
	if s.Mode == "" {
		return ModeLoad
	}
	return s.Mode
}

// HasLoad indica se a execução sobe o k6.
func (s Scenario) HasLoad() bool { m := s.EffectiveMode(); return m == ModeLoad || m == ModeBoth }

// HasChaos indica se a execução injeta as falhas da linha do tempo.
func (s Scenario) HasChaos() bool { m := s.EffectiveMode(); return m == ModeChaos || m == ModeBoth }

// Validate confere o mínimo para o cenário poder rodar.
func (s Scenario) Validate() error {
	var errs []string
	if strings.TrimSpace(s.Name) == "" {
		errs = append(errs, "nome é obrigatório")
	}
	if s.Target.Container == "" {
		errs = append(errs, "escolha o container alvo")
	}
	if !strings.HasPrefix(s.Target.BaseURL, "http://") && !strings.HasPrefix(s.Target.BaseURL, "https://") {
		errs = append(errs, "URL base deve começar com http:// ou https://")
	}
	switch s.EffectiveMode() {
	case ModeLoad, ModeChaos, ModeBoth:
	default:
		errs = append(errs, fmt.Sprintf("modo desconhecido: %q", s.Mode))
	}
	if s.Profile.DurationSeconds < 10 || s.Profile.DurationSeconds > 3600 {
		errs = append(errs, "duração deve estar entre 10 s e 3600 s")
	}
	if s.HasLoad() {
		if len(s.Endpoints) == 0 {
			errs = append(errs, "adicione pelo menos um endpoint")
		}
		for i, e := range s.Endpoints {
			if !strings.HasPrefix(e.Path, "/") {
				errs = append(errs, fmt.Sprintf("endpoint %d: caminho deve começar com /", i+1))
			}
			switch strings.ToUpper(e.Method) {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				errs = append(errs, fmt.Sprintf("endpoint %d: método inválido %q", i+1, e.Method))
			}
		}
		if s.Profile.VUs < 1 || s.Profile.VUs > 5000 {
			errs = append(errs, "VUs deve estar entre 1 e 5000")
		}
		if _, err := s.Profile.Stages(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if s.RequestTimeoutSeconds < 0 || s.RequestTimeoutSeconds > 120 {
		errs = append(errs, "timeout por requisição deve estar entre 1 s e 120 s")
	}
	if s.SLO != nil {
		if s.SLO.P95Ms <= 0 {
			errs = append(errs, "SLO de p95 deve ser maior que zero")
		}
		if s.SLO.ErrorRatePct < 0 || s.SLO.ErrorRatePct > 100 {
			errs = append(errs, "SLO de taxa de erro deve estar entre 0% e 100%")
		}
		if s.SLO.AvailabilityPct < 0 || s.SLO.AvailabilityPct > 100 {
			errs = append(errs, "SLO de disponibilidade deve estar entre 0% e 100%")
		}
	}
	if s.EffectiveMode() == ModeChaos && !strings.HasPrefix(s.ProbePath, "/") {
		errs = append(errs, "no modo só caos, informe o endpoint da sonda (ex.: /health)")
	}
	if s.HasChaos() {
		if len(s.Faults) == 0 {
			errs = append(errs, "adicione pelo menos uma falha na linha do tempo")
		}
		errs = append(errs, validateFaults(s.Faults, s.Profile.DurationSeconds)...)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Stages converte o perfil pronto em stages do k6. A soma das durações é igual à duração total.
func (p Profile) Stages() ([]Stage, error) {
	d, n := p.DurationSeconds, p.VUs
	part := func(f float64) int {
		v := int(float64(d) * f)
		if v < 1 {
			v = 1
		}
		return v
	}
	switch p.Type {
	case ProfileRamp:
		return []Stage{{DurationSeconds: d, Target: n}}, nil
	case ProfilePlateau:
		up, down := part(0.1), part(0.1)
		return []Stage{
			{DurationSeconds: up, Target: n},
			{DurationSeconds: d - up - down, Target: n},
			{DurationSeconds: down, Target: 0},
		}, nil
	case ProfileSpike:
		base := n / 5
		if base < 1 {
			base = 1
		}
		warm, spikeUp, spikeHold, spikeDown := part(0.3), part(0.05), part(0.2), part(0.05)
		return []Stage{
			{DurationSeconds: warm, Target: base},
			{DurationSeconds: spikeUp, Target: n},
			{DurationSeconds: spikeHold, Target: n},
			{DurationSeconds: spikeDown, Target: base},
			{DurationSeconds: d - warm - spikeUp - spikeHold - spikeDown, Target: base},
		}, nil
	default:
		return nil, fmt.Errorf("perfil de carga desconhecido: %q", p.Type)
	}
}
