package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/api"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/runner"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func main() {
	addr := ":" + getenv("PORT", "8080")
	dbPath := getenv("DB_PATH", "data/app.db")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("criando pasta do banco: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("abrindo banco %s: %v", dbPath, err)
	}
	defer st.Close()

	dk, err := dockerx.New()
	if err != nil {
		log.Printf("aviso: Docker não acessível (%v); listagem de containers e execuções desativadas", err)
		dk = nil
	}

	mgr := runner.NewManager(dk, st)
	if img := os.Getenv("K6_IMAGE"); img != "" {
		mgr.K6Image = img
	}
	mgr.Recover(context.Background())

	srv := &api.Server{Docker: dk, Store: st, Runner: mgr}
	log.Printf("backend ouvindo em %s (banco: %s)", addr, dbPath)
	log.Fatal(http.ListenAndServe(addr, srv.Router()))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
