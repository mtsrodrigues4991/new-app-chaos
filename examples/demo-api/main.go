// API de exemplo para testar o app: um endpoint rápido e um que consome CPU.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	http.HandleFunc("/items", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "name": "caneta"}, {"id": 2, "name": "caderno"}})
	})
	http.HandleFunc("/compute", func(w http.ResponseWriter, _ *http.Request) {
		sum := []byte("seed")
		for i := 0; i < 20000; i++ {
			h := sha256.Sum256(sum)
			sum = h[:]
		}
		w.Write([]byte("done"))
	})
	log.Println("demo-api ouvindo em :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
