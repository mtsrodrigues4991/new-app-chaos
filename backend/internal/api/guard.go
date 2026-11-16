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

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Guard são as travas do modo nuvem, onde convidados usam o app:
//
//   - AllowedHosts: a carga e as falhas só miram serviços do próprio cluster, para ninguém
//     usar o cluster para disparar carga contra sites de fora (ALLOWED_TARGET_HOSTS).
//
//   - AIDailyLimit: teto de análises + mensagens de chat por dia (AI_DAILY_LIMIT), para não
//     esgotar a chave da Anthropic.
//
//   - AllowedNamespaces: o alvo e as dependências só podem estar nesses namespaces
//     (ALLOWED_TARGET_NAMESPACES). A URL base sozinha não basta: ela diz para onde vai a carga,
//     e não onde as falhas são injetadas.
//
// Vazio/zero = sem trava (modo local). Os namespaces de sistema são negados sempre, com ou sem
// lista (ver CheckNamespaces).
type Guard struct {
	AllowedHosts      []string
	AllowedNamespaces []string
	AIDailyLimit      int

	mu    sync.Mutex
	day   string
	count int
}

// GuardFromEnv lê ALLOWED_TARGET_HOSTS (sufixos separados por vírgula, ex.: "demo,pix,dorigao"),
// ALLOWED_TARGET_NAMESPACES (nomes exatos separados por vírgula) e AI_DAILY_LIMIT.
func GuardFromEnv() *Guard {
	g := &Guard{}
	for _, h := range strings.Split(os.Getenv("ALLOWED_TARGET_HOSTS"), ",") {
		if h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			g.AllowedHosts = append(g.AllowedHosts, h)
		}
	}
	for _, n := range strings.Split(os.Getenv("ALLOWED_TARGET_NAMESPACES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			g.AllowedNamespaces = append(g.AllowedNamespaces, n)
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

// systemNamespaces nunca são alvo: não aparecem na lista da tela e nenhuma falha entra neles.
// São os que sustentam o cluster, a entrada HTTPS e o próprio motor de falhas.
var systemNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"local-path-storage": true, "chaos-mesh": true, "observability": true,
	"cert-manager": true, "traefik": true, "ingress-nginx": true, "gatekeeper-system": true,
}

// protectedNamespace diz se o namespace é de sistema ou o do próprio app.
func protectedNamespace(ns, ownNS string) bool {
	return systemNamespaces[ns] || strings.HasPrefix(ns, "kube-") || strings.HasPrefix(ns, "openshift-") || (ownNS != "" && ns == ownNS)
}

// CheckNamespaces confere onde o cenário injeta falhas e de quem lê logs: o namespace do alvo
// e o de cada dependência. Vale para qualquer ambiente, inclusive sem trava configurada: os
// namespaces de sistema e o do próprio app (ownNS) são sempre negados.
func (g *Guard) CheckNamespaces(sc scenario.Scenario, ownNS string) error {
	if sc.Target.EffectivePlatform() != scenario.PlatformKubernetes {
		return nil
	}
	check := func(what, ns string) error {
		if protectedNamespace(ns, ownNS) {
			return fmt.Errorf("%s está no namespace %q, que é de sistema ou do próprio app: nenhum teste pode mirar nele", what, ns)
		}
		if g == nil || len(g.AllowedNamespaces) == 0 {
			return nil
		}
		for _, ok := range g.AllowedNamespaces {
			if ns == ok {
				return nil
			}
		}
		return fmt.Errorf("%s está no namespace %q; neste ambiente só são permitidos os namespaces %s",
			what, ns, strings.Join(g.AllowedNamespaces, ", "))
	}
	if err := check("o alvo", sc.Target.Namespace); err != nil {
		return err
	}
	for _, f := range sc.Faults {
		if f.Dependency == nil {
			continue
		}
		if err := check("a dependência "+f.Dependency.Name(), f.Dependency.Namespace); err != nil {
			return err
		}
	}
	return nil
}
