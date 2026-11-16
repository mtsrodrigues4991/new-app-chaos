package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Experimentos A/B: o conteúdo é do pacote experiment; aqui fica só o registro.
const experimentSchema = `
CREATE TABLE IF NOT EXISTS experiments (
  id          TEXT PRIMARY KEY,
  scenario_id TEXT NOT NULL,
  status      TEXT NOT NULL,
  data        TEXT NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS experiments_scenario ON experiments(scenario_id, created_at);
CREATE TABLE IF NOT EXISTS run_experiment (
  run_id        TEXT PRIMARY KEY,
  experiment_id TEXT NOT NULL,
  arm           TEXT NOT NULL
);
`

// RunTag liga uma execução ao experimento que a disparou.
type RunTag struct {
	ExperimentID string `json:"experimentId"`
	Arm          string `json:"arm"` // "A" ou "B"
}

// Estados de um experimento que ainda não terminou.
const (
	ExperimentPending = "pending"
	ExperimentRunning = "running"
)

// SaveExperiment grava (ou substitui) um experimento; v é serializado como JSON.
func (s *Store) SaveExperiment(ctx context.Context, id, scenarioID, status string, createdAt time.Time, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO experiments (id, scenario_id, status, data, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, data = excluded.data`,
		id, scenarioID, status, string(data), ts(createdAt))
	return err
}

// Experiment lê um experimento em v.
func (s *Store) Experiment(ctx context.Context, id string, v any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM experiments WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// Experiments devolve o JSON dos experimentos mais recentes; scenarioID vazio lista todos e
// status vazio não filtra.
func (s *Store) Experiments(ctx context.Context, scenarioID, status string, limit int) ([]json.RawMessage, error) {
	q, args := `SELECT data FROM experiments WHERE 1=1`, []any{}
	if scenarioID != "" {
		q, args = q+` AND scenario_id = ?`, append(args, scenarioID)
	}
	if status != "" {
		q, args = q+` AND status = ?`, append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(data))
	}
	return out, rows.Err()
}

// TagRun registra que a execução pertence a um experimento.
func (s *Store) TagRun(ctx context.Context, runID string, tag RunTag) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO run_experiment (run_id, experiment_id, arm) VALUES (?, ?, ?)`, runID, tag.ExperimentID, tag.Arm)
	return err
}

// RunTagOf devolve o experimento de uma execução (nil para execuções avulsas).
func (s *Store) RunTagOf(ctx context.Context, runID string) (*RunTag, error) {
	var t RunTag
	err := s.db.QueryRowContext(ctx, `SELECT experiment_id, arm FROM run_experiment WHERE run_id = ?`, runID).Scan(&t.ExperimentID, &t.Arm)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
