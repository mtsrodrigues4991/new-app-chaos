// API de exemplo para testar o app: um endpoint rápido e um que consome CPU.
//
// DELAY_MS (opcional) acrescenta um atraso fixo a /items e /compute: serve para simular uma
// versão mais lenta num experimento A/B.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	ms, _ := strconv.Atoi(os.Getenv("DELAY_MS"))
	delay := time.Duration(ms) * time.Millisecond
	slow := func(h http.HandlerFunc) http.HandlerFunc {
		if delay <= 0 {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			h(w, r)
		}
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	http.HandleFunc("/items", slow(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "name": "caneta"}, {"id": 2, "name": "caderno"}})
	}))
	http.HandleFunc("/compute", slow(func(w http.ResponseWriter, _ *http.Request) {
		sum := []byte("seed")
		for i := 0; i < 20000; i++ {
			h := sha256.Sum256(sum)
			sum = h[:]
		}
		w.Write([]byte("done"))
	}))
	log.Printf("demo-api ouvindo em :8080 (atraso artificial: %s)", delay)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
