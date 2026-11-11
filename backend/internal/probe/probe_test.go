package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
)

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var mu sync.Mutex
	var got []load.Probe
	Run(ctx, srv.URL+"/health", 100*time.Millisecond, func(_ time.Time, p load.Probe) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	if len(got) < 2 || !got[0].OK || got[0].Status != 200 {
		t.Fatalf("sondas: %+v", got)
	}

	p := Once(context.Background(), http.DefaultClient, srv.URL+"/x")
	if p.OK || p.Status != 503 {
		t.Errorf("503 deveria ser falha: %+v", p)
	}
	srv.Close()
	p = Once(context.Background(), http.DefaultClient, srv.URL+"/health")
	if p.OK || p.Status != 0 || p.Error == "" {
		t.Errorf("servidor fechado: %+v", p)
	}
}
