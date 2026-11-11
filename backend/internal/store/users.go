package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Usuários e sessões do login (modo nuvem). Só usados com AUTH_ENABLED=true.
const authSchema = `
CREATE TABLE IF NOT EXISTS users (
	username   TEXT PRIMARY KEY,
	hash       TEXT NOT NULL,
	admin      INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	username   TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
	expires_at TEXT NOT NULL
);
`

type User struct {
	Username  string    `json:"username"`
	Hash      string    `json:"-"`
	Admin     bool      `json:"admin"`
	CreatedAt time.Time `json:"createdAt"`
}

// UpsertUser cria o usuário ou troca a senha e o papel de um existente.
func (s *Store) UpsertUser(ctx context.Context, u User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (username, hash, admin, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET hash = excluded.hash, admin = excluded.admin`,
		u.Username, u.Hash, u.Admin, ts(time.Now()))
	return err
}

// CreateUser falha com ErrExists se o nome já estiver em uso.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	if _, err := s.GetUser(ctx, u.Username); err == nil {
		return ErrExists
	}
	return s.UpsertUser(ctx, u)
}

var ErrExists = errors.New("já existe")

func (s *Store) GetUser(ctx context.Context, username string) (User, error) {
	var u User
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT username, hash, admin, created_at FROM users WHERE username = ?`, username).
		Scan(&u.Username, &u.Hash, &u.Admin, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username, admin, created_at FROM users ORDER BY admin DESC, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var created string
		if err := rows.Scan(&u.Username, &u.Admin, &created); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(ctx context.Context, username string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username)
	return err
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, username string, expires time.Time) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(time.Now()))
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, username, expires_at) VALUES (?, ?, ?)`,
		tokenHash, username, ts(expires))
	return err
}

// SessionUser devolve o dono de uma sessão ainda válida.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	var u User
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT u.username, u.admin, s.expires_at FROM sessions s JOIN users u ON u.username = s.username
		WHERE s.token_hash = ?`, tokenHash).Scan(&u.Username, &u.Admin, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if exp, _ := time.Parse(time.RFC3339Nano, expires); time.Now().After(exp) {
		return u, ErrNotFound
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
