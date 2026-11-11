// Package probe faz a sondagem leve do modo só caos: um GET por segundo no endpoint escolhido.
package probe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
)

// Timeout de cada requisição. Curto para não acumular sondas quando o alvo está fora do ar.
const Timeout = 2 * time.Second

// Run dispara um GET em url a cada intervalo até ctx acabar. Cada sonda roda em paralelo,
// então uma resposta lenta não atrasa a próxima. onResult recebe o instante do disparo.
func Run(ctx context.Context, url string, every time.Duration, onResult func(time.Time, load.Probe)) {
	client := &http.Client{
		Timeout: Timeout,
		// Conexão nova a cada sonda: assim uma queda do alvo aparece na hora, como para um cliente novo.
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(every)
	defer t.Stop()
	fire := func(at time.Time) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := Once(ctx, client, url)
			if ctx.Err() == nil { // sonda cortada pelo fim do teste não é resultado
				onResult(at, p)
			}
		}()
	}
	fire(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-t.C:
			fire(at)
		}
	}
}

// Once faz uma sonda.
func Once(ctx context.Context, client *http.Client, url string) load.Probe {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return load.Probe{Error: err.Error()}
	}
	resp, err := client.Do(req)
	ms := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return load.Probe{Ms: ms, Error: shortError(err)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return load.Probe{Ms: ms, Status: resp.StatusCode, OK: resp.StatusCode < 400}
}

func shortError(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "conexão recusada"
	case strings.Contains(err.Error(), "no such host"):
		return "host não encontrado"
	case strings.Contains(err.Error(), "connection reset"):
		return "conexão resetada"
	}
	return err.Error()
}
