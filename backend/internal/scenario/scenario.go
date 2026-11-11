// Package scenario define o modelo de um cenário de teste: alvo, carga, falhas e SLOs.
package scenario

import "time"

// Mode define o que roda numa execução.
type Mode string

const (
	ModeLoad  Mode = "load"  // só carga
	ModeChaos Mode = "chaos" // só caos (com tráfego leve de sondagem)
	ModeBoth  Mode = "both"  // carga + caos na mesma linha do tempo
)

// TargetKind é o tipo de ambiente onde a API roda.
type TargetKind string

const (
	TargetDocker TargetKind = "docker" // MVP
	TargetVM     TargetKind = "vm"     // futuro: VM Linux via SSH
	TargetK8s    TargetKind = "k8s"    // futuro: Kubernetes
)

// Target é a aplicação cuja API será testada.
type Target struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Kind        TargetKind `json:"kind"`
	Ref         string     `json:"ref"`         // id/nome do container no Docker
	BaseURL     string     `json:"baseUrl"`     // ex.: http://demo-api:8080
	HealthCheck string     `json:"healthCheck"` // ex.: /health
}

// Endpoint é uma requisição que o k6 vai disparar.
type Endpoint struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Weight  int               `json:"weight"` // peso relativo entre endpoints
}

// Stage é um trecho do perfil de carga (equivale a um stage do k6).
type Stage struct {
	Duration time.Duration `json:"duration"`
	Target   int           `json:"target"` // usuários virtuais ao fim do trecho
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

// Fault é uma falha posicionada na linha do tempo.
type Fault struct {
	ID       string            `json:"id"`
	Type     FaultType         `json:"type"`
	Start    time.Duration     `json:"start"`    // a partir do início da execução
	Duration time.Duration     `json:"duration"` // depois disso a falha é revertida
	Params   map[string]string `json:"params"`   // ex.: {"delayMs":"200"}
}

// SLO é um critério de aprovação usado no veredito.
type SLO struct {
	Metric    string  `json:"metric"`    // ex.: http_req_duration_p95, error_rate
	Operator  string  `json:"operator"`  // "<" ou "<="
	Threshold float64 `json:"threshold"` // ex.: 300 (ms) ou 0.01
}

// Scenario junta tudo que define um teste.
type Scenario struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	TargetID  string     `json:"targetId"`
	Mode      Mode       `json:"mode"`
	Endpoints []Endpoint `json:"endpoints"`
	Stages    []Stage    `json:"stages"`
	Faults    []Fault    `json:"faults"`
	SLOs      []SLO      `json:"slos"`
}
