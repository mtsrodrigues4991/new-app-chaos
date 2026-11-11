package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

const (
	CookieName = "nac_session"
	sessionTTL = 7 * 24 * time.Hour
	// Tentativas de login erradas por IP antes de bloquear por um tempo.
	maxFailures   = 10
	failureWindow = 15 * time.Minute
	minPassword   = 8
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

type Service struct {
	Store   *store.Store
	Enabled bool
	// SecureCookie força o atributo Secure (atrás do Ingress com HTTPS).
	SecureCookie bool

	mu       sync.Mutex
	failures map[string][]time.Time
}

// FromEnv lê AUTH_ENABLED, ADMIN_USER, ADMIN_PASSWORD, APP_USERS e COOKIE_SECURE e cria
// (ou atualiza) os usuários a cada início, para os convidados fixos sobreviverem a um banco novo.
//
// APP_USERS: um usuário por linha (ou separados por vírgula), no formato nome:senha. A senha
// pode vir em texto ou já como hash (pbkdf2-sha256$...).
func FromEnv(ctx context.Context, st *store.Store) (*Service, error) {
	s := &Service{Store: st, failures: map[string][]time.Time{}}
	s.Enabled = isTrue(os.Getenv("AUTH_ENABLED"))
	s.SecureCookie = isTrue(os.Getenv("COOKIE_SECURE"))
	if !s.Enabled {
		return s, nil
	}
	admin := strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_USER")))
	if admin == "" {
		admin = "admin"
	}
	if pw := os.Getenv("ADMIN_PASSWORD"); pw != "" {
		if err := s.seed(ctx, admin, pw, true); err != nil {
			return nil, fmt.Errorf("ADMIN_PASSWORD: %w", err)
		}
	} else if _, err := st.GetUser(ctx, admin); err != nil {
		return nil, errors.New("AUTH_ENABLED=true exige ADMIN_PASSWORD (senha do administrador)")
	}
	for _, line := range strings.FieldsFunc(os.Getenv("APP_USERS"), func(r rune) bool { return r == '\n' || r == ',' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, pw, ok := strings.Cut(line, ":")
		if !ok {
			log.Printf("aviso: APP_USERS ignorou uma linha sem ':' (formato nome:senha)")
			continue
		}
		if err := s.seed(ctx, strings.ToLower(strings.TrimSpace(name)), pw, false); err != nil {
			log.Printf("aviso: APP_USERS, usuário %q: %v", name, err)
		}
	}
	log.Printf("login ligado (administrador %q)", admin)
	return s, nil
}

func (s *Service) seed(ctx context.Context, name, pw string, admin bool) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("nome inválido %q (use letras minúsculas, números, ponto, hífen)", name)
	}
	hash := pw
	if !IsHash(pw) {
		if len(pw) < minPassword {
			return fmt.Errorf("senha com menos de %d caracteres", minPassword)
		}
		var err error
		if hash, err = HashPassword(pw); err != nil {
			return err
		}
	}
	return s.Store.UpsertUser(ctx, store.User{Username: name, Hash: hash, Admin: admin})
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "sim", "on":
		return true
	}
	return false
}

type ctxKey struct{}

// UserFrom devolve o usuário logado (vazio com o login desligado).
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(store.User)
	return u, ok
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Service) current(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return store.User{}, false
	}
	u, err := s.Store.SessionUser(r.Context(), hashToken(c.Value))
	return u, err == nil
}

// Rotas abertas mesmo com o login ligado.
func public(path string) bool {
	switch path {
	case "/api/health", "/api/auth/login", "/api/auth/logout", "/api/auth/me":
		return true
	}
	return false
}

// Middleware fecha tudo o que não é público. API responde 401; o /grafana manda para a tela de login.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Enabled || public(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		u, ok := s.current(r)
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/grafana") {
				http.Redirect(w, r, "/#/login", http.StatusFound)
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "faça login para continuar"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// Routes registra /api/auth/* e /api/users/* (este só para administradores).
func (s *Service) Routes(r chi.Router) {
	r.Post("/api/auth/login", s.login)
	r.Post("/api/auth/logout", s.logout)
	r.Get("/api/auth/me", s.me)
	r.Get("/api/users", s.adminOnly(s.listUsers))
	r.Post("/api/users", s.adminOnly(s.createUser))
	r.Delete("/api/users/{name}", s.adminOnly(s.deleteUser))
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"authEnabled": s.Enabled}
	if s.Enabled {
		if u, ok := s.current(r); ok {
			out["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func clientIP(r *http.Request) string {
	// Atrás do Ingress, o IP real vem no X-Forwarded-For (primeiro da lista).
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		ip, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Service) blocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-failureWindow)
	kept := s.failures[ip][:0]
	for _, t := range s.failures[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.failures[ip] = kept
	return len(kept) >= maxFailures
}

func (s *Service) fail(ip string) {
	s.mu.Lock()
	s.failures[ip] = append(s.failures[ip], time.Now())
	s.mu.Unlock()
}

func (s *Service) secure(r *http.Request) bool {
	return s.SecureCookie || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "login desligado neste ambiente"})
		return
	}
	ip := clientIP(r)
	if s.blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "muitas tentativas; espere 15 minutos"})
		return
	}
	var in struct{ Username, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON inválido"})
		return
	}
	u, err := s.Store.GetUser(r.Context(), strings.ToLower(strings.TrimSpace(in.Username)))
	if err != nil || !CheckPassword(u.Hash, in.Password) {
		s.fail(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "usuário ou senha incorretos"})
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	token := hex.EncodeToString(buf)
	exp := time.Now().Add(sessionTTL)
	if err := s.Store.CreateSession(r.Context(), hashToken(token), u.Username, exp); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", Expires: exp, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"authEnabled": true, "user": u})
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		_ = s.Store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "login desligado neste ambiente"})
			return
		}
		if u, ok := UserFrom(r.Context()); !ok || !u.Admin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "só o administrador gerencia usuários"})
			return
		}
		h(w, r)
	}
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username, Password string
		Admin              bool
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON inválido"})
		return
	}
	name := strings.ToLower(strings.TrimSpace(in.Username))
	if !validName.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nome inválido: 2 a 32 caracteres, letras minúsculas, números, ponto, hífen ou _"})
		return
	}
	if len(in.Password) < minPassword {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("a senha precisa de pelo menos %d caracteres", minPassword)})
		return
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	u := store.User{Username: name, Hash: hash, Admin: in.Admin, CreatedAt: time.Now()}
	if err := s.Store.CreateUser(r.Context(), u); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrExists) {
			status = http.StatusConflict
			err = fmt.Errorf("o usuário %q já existe", name)
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Service) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if me, _ := UserFrom(r.Context()); me.Username == name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "você não pode apagar o próprio usuário"})
		return
	}
	if err := s.Store.DeleteUser(r.Context(), name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
