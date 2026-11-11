package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Diagnostics são os logs e eventos coletados do alvo (e das dependências) durante uma
// execução. Os instantes (T) são segundos desde o início da execução, como na série.
type Diagnostics struct {
	Logs   []PodLog    `json:"logs"`
	Events []KubeEvent `json:"events"`
	// Notes conta o que não deu para coletar (ex.: sem permissão para ler eventos).
	Notes []string `json:"notes,omitempty"`
}

// PodLog é o log de um container (no Docker, o próprio container alvo).
type PodLog struct {
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	// Role diz de quem é: "alvo" ou "dependência ns/deployment".
	Role string `json:"role"`
	// Previous marca o log da instância anterior de um container que reiniciou.
	Previous bool `json:"previous,omitempty"`
	// Gone marca um pod que deixou de existir durante a execução (ex.: apagado pelo kill);
	// o log vai até a última coleta antes disso.
	Gone      bool      `json:"gone,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Lines     []LogLine `json:"lines"`
}

type LogLine struct {
	T    float64 `json:"t"`
	Text string  `json:"text"`
}

// KubeEvent é um evento do Kubernetes (ex.: Killing, BackOff, Unhealthy, FailedScheduling).
type KubeEvent struct {
	T         float64 `json:"t"`
	Type      string  `json:"type"` // Normal ou Warning
	Reason    string  `json:"reason"`
	Namespace string  `json:"namespace"`
	Object    string  `json:"object"` // Kind/nome
	Message   string  `json:"message"`
	Count     int     `json:"count,omitempty"`
}

func (s *Store) SaveDiagnostics(ctx context.Context, runID string, d Diagnostics) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO run_diagnostics (run_id, data) VALUES (?, ?)`, runID, string(data))
	return err
}

// Diagnostics devolve os logs e eventos de uma execução; nil se não houver (execuções antigas).
func (s *Store) Diagnostics(ctx context.Context, runID string) (*Diagnostics, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM run_diagnostics WHERE run_id = ?`, runID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d Diagnostics
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, err
	}
	return &d, nil
}
