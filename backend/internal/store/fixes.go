package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Tentativas de correção automática: o conteúdo é do pacote fix; aqui fica só o registro,
// que é também a trilha de auditoria (quem pediu, quem aprovou, diffs, logs e custo).
const fixSchema = `
CREATE TABLE IF NOT EXISTS fixes (
  id         TEXT PRIMARY KEY,
  run_id     TEXT NOT NULL,
  status     TEXT NOT NULL,
  data       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS fixes_run ON fixes(run_id, created_at);
`

// SaveFix grava (ou substitui) uma tentativa; v é serializado como JSON.
func (s *Store) SaveFix(ctx context.Context, id, runID, status string, createdAt time.Time, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO fixes (id, run_id, status, data, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, data = excluded.data`,
		id, runID, status, string(data), ts(createdAt))
	return err
}

// Fix lê uma tentativa em v.
func (s *Store) Fix(ctx context.Context, id string, v any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM fixes WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// Fixes devolve o JSON das tentativas mais recentes; runID e status vazios não filtram.
func (s *Store) Fixes(ctx context.Context, runID, status string, limit int) ([]json.RawMessage, error) {
	q, args := `SELECT data FROM fixes WHERE 1=1`, []any{}
	if runID != "" {
		q, args = q+` AND run_id = ?`, append(args, runID)
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
