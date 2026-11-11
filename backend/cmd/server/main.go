package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ai"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/api"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
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
		log.Printf("aviso: Docker não acessível (%v); alvos container desativados", err)
		dk = nil
	}

	kc, err := kube.New()
	if err != nil {
		log.Printf("aviso: Kubernetes não acessível (%v); alvos Deployment desativados", err)
		kc = nil
	} else if kc != nil {
		log.Printf("Kubernetes: API em %s, namespace do app %q", kc.Base, kc.Namespace())
	}

	mgr := runner.NewManager(dk, kc, st)
	if img := os.Getenv("K6_IMAGE"); img != "" {
		mgr.K6Image = img
	}
	// Injeção de falhas: o stressor vai junto na imagem do backend, e a própria imagem do
	// backend (que tem tc) serve de auxiliar de rede. Fora do Docker, use as variáveis.
	mgr.Chaos.StressorPath = getenv("STRESSOR_PATH", "/usr/local/lib/new-app-chaos/stressor")
	if img := os.Getenv("TOOLS_IMAGE"); img != "" {
		mgr.Chaos.ToolsImages = []string{img}
	} else if dk != nil {
		_, mgr.Chaos.ToolsImages = dk.Self(context.Background())
	}
	if dk != nil {
		log.Printf("imagem auxiliar das falhas de rede: %v", mgr.Chaos.ToolsImages)
	}
	mgr.Recover(context.Background())

	// IA: a chave vem só do ambiente (.env). Sem chave, o app funciona e o botão da IA avisa.
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Printf("aviso: ANTHROPIC_API_KEY não definida; análise por IA desativada até configurar o .env")
	}
	reg := ai.NewRegistry(ai.NewAnthropic(key))

	// Código da aplicação alvo (GitHub), lido pela IA. O token é opcional (repositórios públicos).
	code := &coderepo.Fetcher{
		Token:    os.Getenv("GITHUB_TOKEN"),
		APIBase:  os.Getenv("GITHUB_API_URL"),
		CacheDir: getenv("CODE_CACHE_DIR", filepath.Join(filepath.Dir(dbPath), "code")),
	}

	srv := &api.Server{Docker: dk, Kube: kc, Store: st, Runner: mgr, AI: reg, Code: code}
	log.Printf("backend ouvindo em %s (banco: %s)", addr, dbPath)
	log.Fatal(http.ListenAndServe(addr, srv.Router()))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
