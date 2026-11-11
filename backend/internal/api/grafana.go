package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// GrafanaProxy repassa /grafana/... para o Grafana do cluster, para o navegador abrir o
// Grafana pelo mesmo endereço do app (sem port-forward). O Grafana precisa estar configurado
// para servir em /grafana (GF_SERVER_SERVE_FROM_SUB_PATH), então o caminho vai sem mudança.
func GrafanaProxy(target string) (http.Handler, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("endereço do Grafana inválido: %q", target)
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "Grafana fora do ar ("+err.Error()+"). Ele só existe com OBS=1 ./scripts/kind-up.sh.", http.StatusBadGateway)
	}
	return p, nil
}
