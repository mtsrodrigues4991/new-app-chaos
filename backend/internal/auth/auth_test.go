package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("segredo-forte")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHash(h) || !CheckPassword(h, "segredo-forte") || CheckPassword(h, "errada") || CheckPassword("lixo", "x") {
		t.Fatalf("hash/check incoerentes: %s", h)
	}
}

func newServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("ADMIN_USER", "mateus")
	t.Setenv("ADMIN_PASSWORD", "senha-do-admin")
	t.Setenv("APP_USERS", "ana:senha-da-ana\ncurta:123")
	s, err := FromEnv(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(s.Middleware)
	s.Routes(r)
	r.Get("/api/scenarios", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("[]")) })
	r.Get("/grafana/*", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("grafana")) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s
}

func do(t *testing.T, method, url, body, cookie string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	res, err := http.DefaultTransport.RoundTrip(req) // sem seguir redirecionamentos
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestLoginFlow(t *testing.T) {
	srv, s := newServer(t)
	if res := do(t, "GET", srv.URL+"/api/scenarios", "", ""); res.StatusCode != 401 {
		t.Fatalf("sem login: %d", res.StatusCode)
	}
	if res := do(t, "GET", srv.URL+"/grafana/x", "", ""); res.StatusCode != 302 {
		t.Fatalf("grafana sem login: %d", res.StatusCode)
	}
	if res := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"mateus","password":"errada"}`, ""); res.StatusCode != 401 {
		t.Fatalf("senha errada: %d", res.StatusCode)
	}
	res := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"Mateus","password":"senha-do-admin"}`, "")
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatalf("login: %d", res.StatusCode)
	}
	c := res.Cookies()[0]
	if !c.HttpOnly {
		t.Fatal("cookie sem HttpOnly")
	}
	if res := do(t, "GET", srv.URL+"/api/scenarios", "", c.Value); res.StatusCode != 200 {
		t.Fatalf("com login: %d", res.StatusCode)
	}
	if res := do(t, "GET", srv.URL+"/grafana/x", "", c.Value); res.StatusCode != 200 {
		t.Fatalf("grafana com login: %d", res.StatusCode)
	}
	// Admin cria convidado; convidado não gerencia usuários.
	if res := do(t, "POST", srv.URL+"/api/users", `{"username":"bia","password":"senha-da-bia"}`, c.Value); res.StatusCode != 201 {
		t.Fatalf("criar usuário: %d", res.StatusCode)
	}
	if res := do(t, "POST", srv.URL+"/api/users", `{"username":"bia","password":"senha-da-bia"}`, c.Value); res.StatusCode != 409 {
		t.Fatalf("usuário repetido: %d", res.StatusCode)
	}
	res = do(t, "POST", srv.URL+"/api/auth/login", `{"username":"ana","password":"senha-da-ana"}`, "")
	if res.StatusCode != 200 {
		t.Fatalf("login do APP_USERS: %d", res.StatusCode)
	}
	ana := res.Cookies()[0].Value
	if res := do(t, "GET", srv.URL+"/api/users", "", ana); res.StatusCode != 403 {
		t.Fatalf("convidado listando usuários: %d", res.StatusCode)
	}
	if _, err := s.Store.GetUser(context.Background(), "curta"); err == nil {
		t.Fatal("senha curta do APP_USERS não deveria criar usuário")
	}
	// Logout invalida a sessão.
	do(t, "POST", srv.URL+"/api/auth/logout", "", ana)
	if res := do(t, "GET", srv.URL+"/api/scenarios", "", ana); res.StatusCode != 401 {
		t.Fatalf("depois do logout: %d", res.StatusCode)
	}
	// Apagar usuário derruba as sessões dele.
	if res := do(t, "DELETE", srv.URL+"/api/users/mateus", "", c.Value); res.StatusCode != 400 {
		t.Fatalf("apagar a si mesmo: %d", res.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv, _ := newServer(t)
	for i := 0; i < maxFailures; i++ {
		do(t, "POST", srv.URL+"/api/auth/login", `{"username":"mateus","password":"errada"}`, "")
	}
	if res := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"mateus","password":"senha-do-admin"}`, ""); res.StatusCode != 429 {
		t.Fatalf("depois de %d erros: %d", maxFailures, res.StatusCode)
	}
}

func TestDisabledIsOpen(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "app.db"))
	defer st.Close()
	t.Setenv("AUTH_ENABLED", "")
	s, err := FromEnv(context.Background(), st)
	if err != nil || s.Enabled {
		t.Fatalf("deveria vir desligado: %v", err)
	}
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/scenarios", nil))
	if rec.Code != 204 {
		t.Fatalf("desligado deveria passar: %d", rec.Code)
	}
}
