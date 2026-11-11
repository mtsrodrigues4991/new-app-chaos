// Package store guarda cenários, execuções e métricas em SQLite.
// O acesso fica atrás desta API para trocar por PostgreSQL quando virar SaaS.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite" // driver SQLite em Go puro (sem CGO)

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

var ErrNotFound = errors.New("não encontrado")

// RunStatus é o estado de uma execução.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunAborted   RunStatus = "aborted"
	RunFailed    RunStatus = "failed"
)

// Run é uma execução de um cenário. Guarda uma cópia do cenário como estava ao rodar.
type Run struct {
	ID           string            `json:"id"`
	ScenarioID   string            `json:"scenarioId"`
	ScenarioName string            `json:"scenarioName"`
	Scenario     scenario.Scenario `json:"scenario"`
	Status       RunStatus         `json:"status"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	StartedAt    *time.Time        `json:"startedAt,omitempty"`
	EndedAt      *time.Time        `json:"endedAt,omitempty"`
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS scenarios (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  data       TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id          TEXT PRIMARY KEY,
  scenario_id TEXT NOT NULL,
  scenario    TEXT NOT NULL,
  status      TEXT NOT NULL,
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  ended_at    TEXT
);
CREATE INDEX IF NOT EXISTS runs_scenario ON runs(scenario_id, created_at);
CREATE TABLE IF NOT EXISTS run_metrics (
  run_id TEXT NOT NULL,
  t      INTEGER NOT NULL,
  data   TEXT NOT NULL,
  PRIMARY KEY (run_id, t)
);
CREATE TABLE IF NOT EXISTS run_faults (
  run_id   TEXT NOT NULL,
  fault_id TEXT NOT NULL,
  data     TEXT NOT NULL,
  PRIMARY KEY (run_id, fault_id)
);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_analyses (
  run_id     TEXT PRIMARY KEY,
  data       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_chat (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     TEXT NOT NULL,
  role       TEXT NOT NULL,
  content    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_chat_run ON run_chat(run_id, id);
CREATE TABLE IF NOT EXISTS run_diagnostics (
  run_id TEXT PRIMARY KEY,
  data   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS scenario_baselines (
  scenario_id TEXT PRIMARY KEY,
  run_id      TEXT NOT NULL
);
`

// Open abre (ou cria) o banco no caminho indicado.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: um escritor por vez evita "database is locked"
	if _, err := db.Exec(schema + authSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	return &t
}

// --- cenários ---

func (s *Store) SaveScenario(ctx context.Context, sc scenario.Scenario) error {
	data, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO scenarios (id, name, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, data = excluded.data, updated_at = excluded.updated_at`,
		sc.ID, sc.Name, string(data), ts(time.Now()))
	return err
}

func (s *Store) GetScenario(ctx context.Context, id string) (scenario.Scenario, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM scenarios WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return scenario.Scenario{}, ErrNotFound
	}
	if err != nil {
		return scenario.Scenario{}, err
	}
	var sc scenario.Scenario
	return sc, json.Unmarshal([]byte(data), &sc)
}

func (s *Store) ListScenarios(ctx context.Context) ([]scenario.Scenario, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM scenarios ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scenario.Scenario{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var sc scenario.Scenario
		if err := json.Unmarshal([]byte(data), &sc); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) DeleteScenario(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM scenarios WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM scenario_baselines WHERE scenario_id = ?`, id)
	return err
}

// --- execuções ---

func (s *Store) CreateRun(ctx context.Context, r Run) error {
	data, err := json.Marshal(r.Scenario)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (id, scenario_id, scenario, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		r.ID, r.ScenarioID, string(data), string(r.Status), ts(r.CreatedAt))
	return err
}

// SetRunStatus atualiza o estado; started/ended só são gravados quando não nulos.
func (s *Store) SetRunStatus(ctx context.Context, id string, st RunStatus, errMsg string, started, ended *time.Time) error {
	var sv, ev any
	if started != nil {
		sv = ts(*started)
	}
	if ended != nil {
		ev = ts(*ended)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?,
		started_at = COALESCE(?, started_at), ended_at = COALESCE(?, ended_at) WHERE id = ?`,
		string(st), errMsg, sv, ev, id)
	return err
}

const runCols = `id, scenario_id, scenario, status, error, created_at, started_at, ended_at`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var data, status, created string
	var started, ended sql.NullString
	if err := sc.Scan(&r.ID, &r.ScenarioID, &data, &status, &r.Error, &created, &started, &ended); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(data), &r.Scenario); err != nil {
		return r, err
	}
	r.ScenarioName = r.Scenario.Name
	r.Status = RunStatus(status)
	if t := parseTS(sql.NullString{String: created, Valid: true}); t != nil {
		r.CreatedAt = *t
	}
	r.StartedAt, r.EndedAt = parseTS(started), parseTS(ended)
	return r, nil
}

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}

// ListRuns lista as execuções mais recentes; scenarioID vazio lista todas.
func (s *Store) ListRuns(ctx context.Context, scenarioID string, limit int) ([]Run, error) {
	q := `SELECT ` + runCols + ` FROM runs`
	args := []any{}
	if scenarioID != "" {
		q += ` WHERE scenario_id = ?`
		args = append(args, scenarioID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InterruptedRuns lista as execuções que estavam ativas quando o backend parou
// (para reverter falhas que possam ter ficado aplicadas no alvo).
func (s *Store) InterruptedRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM runs WHERE status IN (?, ?)`, string(RunPending), string(RunRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailInterrupted marca como falhas as execuções que estavam rodando quando o backend parou.
func (s *Store) FailInterrupted(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, ended_at = COALESCE(ended_at, ?)
		WHERE status IN (?, ?)`, string(RunFailed), "interrompida: o backend reiniciou durante a execução",
		ts(time.Now()), string(RunPending), string(RunRunning))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- métricas ---

func (s *Store) AddWindows(ctx context.Context, runID string, ws []load.Window) error {
	if len(ws) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range ws {
		data, err := json.Marshal(w)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO run_metrics (run_id, t, data) VALUES (?, ?, ?)`, runID, w.T, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Windows(ctx context.Context, runID string) ([]load.Window, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM run_metrics WHERE run_id = ? ORDER BY t`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []load.Window{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var w load.Window
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// --- falhas injetadas ---

// FaultState é o que aconteceu de fato com uma falha da linha do tempo numa execução.
type FaultState string

const (
	FaultActive   FaultState = "active"   // aplicada, ainda não revertida
	FaultReverted FaultState = "reverted" // aplicada e revertida
	FaultFailed   FaultState = "failed"   // não pôde ser aplicada ou revertida
	FaultSkipped  FaultState = "skipped"  // não chegou a rodar (execução terminou antes)
)

// FaultEvent registra os instantes reais (em segundos desde o início) de uma falha.
type FaultEvent struct {
	FaultID    string             `json:"faultId"`
	Type       scenario.FaultType `json:"type"`
	State      FaultState         `json:"state"`
	AppliedAt  *float64           `json:"appliedAt,omitempty"`
	RevertedAt *float64           `json:"revertedAt,omitempty"`
	Error      string             `json:"error,omitempty"`
}

func (s *Store) SaveFaultEvent(ctx context.Context, runID string, ev FaultEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO run_faults (run_id, fault_id, data) VALUES (?, ?, ?)`, runID, ev.FaultID, string(data))
	return err
}

func (s *Store) FaultEvents(ctx context.Context, runID string) ([]FaultEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM run_faults WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FaultEvent{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var ev FaultEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
