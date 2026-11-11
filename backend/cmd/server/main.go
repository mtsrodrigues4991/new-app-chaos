package main

import (
	"log"
	"net/http"
	"os"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/api"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func main() {
	addr := ":" + getenv("PORT", "8080")

	dk, err := dockerx.New()
	if err != nil {
		log.Printf("aviso: Docker não acessível (%v); listagem de containers desativada", err)
		dk = nil
	}

	srv := &api.Server{Docker: dk, Store: store.NewMemory()}
	log.Printf("backend ouvindo em %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Router()))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
