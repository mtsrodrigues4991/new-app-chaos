// Package coderepo baixa o código da aplicação alvo do GitHub e oferece à IA ferramentas
// só de leitura (listar pastas, buscar texto, abrir arquivo) sobre uma cópia local.
package coderepo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Limites da cópia local: o que passa disso não ajuda a IA e só ocupa disco.
const (
	MaxFileBytes  = 1 << 20   // arquivos maiores ficam de fora
	MaxTotalBytes = 300 << 20 // tamanho máximo do repositório extraído
	MaxFiles      = 30000
)

// Pastas que nunca são extraídas (dependências, saídas de build, metadados).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "build": true, "dist": true, "out": true,
	"vendor": true, ".gradle": true, ".idea": true, ".vscode": true, ".mvn": true, "__pycache__": true,
	".venv": true, "venv": true, "obj": true, ".next": true, "coverage": true,
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// NormalizeRepo aceita "dono/repo" ou a URL do GitHub e devolve "dono/repo".
func NormalizeRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/", "git@github.com:"} {
		s = strings.TrimPrefix(s, p)
	}
	if !repoRe.MatchString(s) {
		return "", fmt.Errorf("repositório inválido %q: use dono/repo ou a URL do GitHub", s)
	}
	return s, nil
}

// Fetcher baixa repositórios do GitHub para uma pasta de cache.
type Fetcher struct {
	Token    string // GITHUB_TOKEN (opcional; necessário para repositórios privados)
	APIBase  string // padrão https://api.github.com
	CacheDir string
	Client   *http.Client

	mu sync.Mutex // um download por vez
}

// Snapshot é o código de um commit, extraído na pasta Root.
type Snapshot struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path,omitempty"` // subpasta usada como raiz
	Commit string `json:"commit"`
	Root   string `json:"-"`
}

// URL devolve o link do arquivo (e da linha) no GitHub, no commit analisado.
func (s Snapshot) URL(file string, line int) string {
	p := path.Join(s.Path, file)
	u := fmt.Sprintf("https://github.com/%s/blob/%s/%s", s.Repo, s.Commit, p)
	if line > 0 {
		u += fmt.Sprintf("#L%d", line)
	}
	return u
}

func (f *Fetcher) api() string {
	if f.APIBase != "" {
		return strings.TrimRight(f.APIBase, "/")
	}
	return "https://api.github.com"
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (f *Fetcher) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "new-app-chaos")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.Token)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("acessando o GitHub: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		if f.Token == "" {
			return nil, errors.New("GitHub respondeu 404: o repositório ou o branch não existe, ou o repositório é privado e falta o GITHUB_TOKEN no .env")
		}
		return nil, errors.New("GitHub respondeu 404: o repositório ou o branch não existe, ou o GITHUB_TOKEN não tem acesso a ele")
	case http.StatusUnauthorized:
		return nil, errors.New("GitHub recusou o GITHUB_TOKEN (401); confira o .env")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub recusou o acesso (%d); pode ser limite de uso da API sem token: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil, fmt.Errorf("GitHub respondeu %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// Fetch resolve o commit atual do branch e garante a cópia local dele.
func (f *Fetcher) Fetch(ctx context.Context, repo, branch, sub string) (Snapshot, error) {
	repo, err := NormalizeRepo(repo)
	if err != nil {
		return Snapshot{}, err
	}
	if branch == "" {
		branch = "main"
	}
	sub = strings.Trim(path.Clean("/"+sub), "/")
	snap := Snapshot{Repo: repo, Branch: branch, Path: sub}

	resp, err := f.get(ctx, fmt.Sprintf("%s/repos/%s/commits/%s", f.api(), repo, url.PathEscape(branch)), "application/vnd.github.sha")
	if err != nil {
		return snap, err
	}
	sha, err := io.ReadAll(io.LimitReader(resp.Body, 100))
	resp.Body.Close()
	if err != nil {
		return snap, err
	}
	snap.Commit = strings.TrimSpace(string(sha))
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(snap.Commit) {
		return snap, fmt.Errorf("GitHub devolveu um commit inesperado: %q", snap.Commit)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	repoDir := filepath.Join(f.CacheDir, strings.ReplaceAll(repo, "/", "__"))
	dir := filepath.Join(repoDir, snap.Commit)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err != nil {
		if err := f.download(ctx, repo, snap.Commit, dir); err != nil {
			os.RemoveAll(dir)
			return snap, err
		}
		// Guarda só o commit mais recente de cada repositório.
		if entries, err := os.ReadDir(repoDir); err == nil {
			for _, e := range entries {
				if e.Name() != snap.Commit {
					os.RemoveAll(filepath.Join(repoDir, e.Name()))
				}
			}
		}
	}
	snap.Root = filepath.Join(dir, "src", filepath.FromSlash(sub))
	st, err := os.Stat(snap.Root)
	if err != nil || !st.IsDir() {
		return snap, fmt.Errorf("a subpasta %q não existe no repositório", sub)
	}
	return snap, nil
}

func (f *Fetcher) download(ctx context.Context, repo, sha, dir string) error {
	resp, err := f.get(ctx, fmt.Sprintf("%s/repos/%s/tarball/%s", f.api(), repo, sha), "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("lendo o pacote do repositório: %w", err)
	}
	root := filepath.Join(dir, "src")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("lendo o pacote do repositório: %w", err)
		}
		// O GitHub põe tudo numa pasta "dono-repo-sha/"; ela é descartada.
		name := h.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		name = path.Clean(name)
		if name == "." || name == "" || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			continue
		}
		if skipped(name) {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size > MaxFileBytes {
				continue
			}
			files++
			total += h.Size
			if files > MaxFiles || total > MaxTotalBytes {
				return fmt.Errorf("o repositório passa do limite de %d arquivos ou %d MB; use a subpasta da aplicação", MaxFiles, MaxTotalBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, MaxFileBytes))
			out.Close()
			if err != nil {
				return err
			}
		default:
			// Links simbólicos e outros tipos ficam de fora: nada pode apontar para fora da cópia.
		}
	}
	return os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

func skipped(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}
