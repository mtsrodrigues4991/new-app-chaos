package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Guard são as travas do modo nuvem, onde convidados usam o app:
//   - AllowedHosts: a carga e as falhas só miram serviços do próprio cluster, para ninguém
//     usar o cluster para disparar carga contra sites de fora (ALLOWED_TARGET_HOSTS).
//   - AIDailyLimit: teto de análises + mensagens de chat por dia (AI_DAILY_LIMIT), para não
//     esgotar a chave da Anthropic.
//
// Vazio/zero = sem trava (modo local).
type Guard struct {
	AllowedHosts []string
	AIDailyLimit int

	mu    sync.Mutex
	day   string
	count int
}

// GuardFromEnv lê ALLOWED_TARGET_HOSTS (sufixos separados por vírgula, ex.: "demo,pix,dorigao")
// e AI_DAILY_LIMIT.
func GuardFromEnv() *Guard {
	g := &Guard{}
	for _, h := range strings.Split(os.Getenv("ALLOWED_TARGET_HOSTS"), ",") {
		if h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			g.AllowedHosts = append(g.AllowedHosts, h)
		}
	}
	g.AIDailyLimit, _ = strconv.Atoi(os.Getenv("AI_DAILY_LIMIT"))
	return g
}

// CheckTarget aceita o host igual a um sufixo ou terminado em ".<sufixo>".
func (g *Guard) CheckTarget(base string) error {
	if g == nil || len(g.AllowedHosts) == 0 {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("URL base inválida: %q", base)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	// Nome completo do Service (pix-api.pix.svc.cluster.local) vale como o curto.
	host = strings.TrimSuffix(strings.TrimSuffix(host, ".cluster.local"), ".svc")
	for _, h := range g.AllowedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("neste ambiente a URL base precisa ser de um serviço do cluster (terminando em %s), não %q",
		strings.Join(g.AllowedHosts, ", "), host)
}

// TakeAI consome uma chamada da cota do dia; false quando acabou.
func (g *Guard) TakeAI() bool {
	if g == nil || g.AIDailyLimit <= 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if g.day != today {
		g.day, g.count = today, 0
	}
	if g.count >= g.AIDailyLimit {
		return false
	}
	g.count++
	return true
}

func (s *Server) aiAllowed(w http.ResponseWriter) bool {
	if s.Guard.TakeAI() {
		return true
	}
	writeErr(w, http.StatusTooManyRequests, fmt.Errorf("limite diário de %d usos da IA neste ambiente atingido; volta amanhã (UTC)", s.Guard.AIDailyLimit))
	return false
}
