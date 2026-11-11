package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// --- configurações ---

// Setting lê uma configuração; devolve def quando não existe.
func (s *Store) Setting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- execução de referência (baseline) por cenário ---

// Baseline devolve o id da execução de referência do cenário ("" se não houver).
func (s *Store) Baseline(ctx context.Context, scenarioID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM scenario_baselines WHERE scenario_id = ?`, scenarioID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SetBaseline marca (runID != "") ou desmarca (runID == "") a referência do cenário.
func (s *Store) SetBaseline(ctx context.Context, scenarioID, runID string) error {
	if runID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM scenario_baselines WHERE scenario_id = ?`, scenarioID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO scenario_baselines (scenario_id, run_id) VALUES (?, ?)
		ON CONFLICT(scenario_id) DO UPDATE SET run_id = excluded.run_id`, scenarioID, runID)
	return err
}

// --- relatório da IA ---

// SaveAnalysis guarda o relatório (qualquer JSON) de uma execução, substituindo o anterior.
func (s *Store) SaveAnalysis(ctx context.Context, runID string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO run_analyses (run_id, data, created_at) VALUES (?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET data = excluded.data, created_at = excluded.created_at`, runID, string(data), ts(time.Now()))
	return err
}

// Analysis lê o relatório para dst; devolve ErrNotFound se a execução não foi analisada.
func (s *Store) Analysis(ctx context.Context, runID string, dst any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM run_analyses WHERE run_id = ?`, runID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), dst)
}

// --- chat sobre a execução ---

// ChatMessage é uma mensagem salva do chat de uma execução.
type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"` // user ou assistant
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) AddChatMessage(ctx context.Context, runID, role, content string) (ChatMessage, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO run_chat (run_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		runID, role, content, ts(now))
	if err != nil {
		return ChatMessage{}, err
	}
	id, _ := res.LastInsertId()
	return ChatMessage{ID: id, Role: role, Content: content, CreatedAt: now}, nil
}

func (s *Store) ChatMessages(ctx context.Context, runID string) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, content, created_at FROM run_chat WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMessage{}
	for rows.Next() {
		var m ChatMessage
		var created string
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &created); err != nil {
			return nil, err
		}
		if t := parseTS(sql.NullString{String: created, Valid: true}); t != nil {
			m.CreatedAt = *t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteChatMessage apaga uma mensagem (usado quando a IA falha e a pergunta fica sem resposta).
func (s *Store) DeleteChatMessage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_chat WHERE id = ?`, id)
	return err
}

// ClearChat apaga a conversa de uma execução.
func (s *Store) ClearChat(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_chat WHERE run_id = ?`, runID)
	return err
}
