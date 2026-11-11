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
type Fault struct {
	ID              string            `json:"id"`
	Type            FaultType         `json:"type"`
	StartSeconds    int               `json:"startSeconds"`    // a partir do início da execução
	DurationSeconds int               `json:"durationSeconds"` // depois disso a falha é revertida
	Params          map[string]string `json:"params"`          // ex.: {"delayMs":"200"}
}

// SLO é um critério de aprovação usado no veredito (marco 4).
type SLO struct {
	Metric    string  `json:"metric"`    // ex.: p95_ms, error_rate
	Operator  string  `json:"operator"`  // "<" ou "<="
	Threshold float64 `json:"threshold"` // ex.: 300 (ms) ou 0.01
}

// Scenario junta tudo que define um teste.
type Scenario struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Target    Target     `json:"target"`
	Mode      Mode       `json:"mode"`
	Endpoints []Endpoint `json:"endpoints"`
	Profile   Profile    `json:"profile"`
	Faults    []Fault    `json:"faults,omitempty"`
	SLOs      []SLO      `json:"slos,omitempty"`
}

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
	if s.Profile.DurationSeconds < 10 || s.Profile.DurationSeconds > 3600 {
		errs = append(errs, "duração deve estar entre 10 s e 3600 s")
	}
	if _, err := s.Profile.Stages(); err != nil {
		errs = append(errs, err.Error())
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
