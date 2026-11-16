// Package ghpr publica uma correção no GitHub: cria um branch com um commit e abre um PR em
// rascunho. É o único código que segura a credencial de escrita, e não tem modelo nenhum: o
// agente que lê conteúdo não confiável (código, logs) produz só um diff, e quem publica é este
// passo determinístico.
//
// Limites que valem aqui, e não por convenção: o branch sempre começa com "nac/", nunca é o
// branch de destino, o PR é sempre rascunho, e não existe função de merge nem de aprovação.
package ghpr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// BranchPrefix é o prefixo obrigatório dos branches criados pelo app.
const BranchPrefix = "nac/"

// ErrNotConfigured indica que falta o token de escrita.
var ErrNotConfigured = errors.New("falta o PR_GITHUB_TOKEN (token com contents:write e pull_requests:write nos repositórios alvo)")

// Client fala com a API do GitHub usando o token de escrita.
type Client struct {
	Token   string // PR_GITHUB_TOKEN: separado do token de leitura do código
	APIBase string // padrão https://api.github.com
	HTTP    *http.Client
}

// Configured diz se há token de escrita.
func (c *Client) Configured() bool { return c != nil && c.Token != "" }

// Change é um arquivo do commit: o conteúdo novo, ou a remoção.
type Change struct {
	Path    string
	Content []byte
	Delete  bool
}

// PR é o pull request aberto.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// Request descreve o PR a abrir.
type Request struct {
	Repo       string // dono/repo
	BaseBranch string // para onde o PR aponta
	BaseCommit string // commit sobre o qual o diff foi feito (o que rodou no teste)
	Branch     string // nac/...
	Title      string
	Body       string
	Message    string // mensagem do commit
	Changes    []Change
}

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchRe = regexp.MustCompile(`^nac/[A-Za-z0-9._-]+$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func (r Request) validate() error {
	switch {
	case !repoRe.MatchString(r.Repo):
		return fmt.Errorf("repositório inválido: %q", r.Repo)
	case !branchRe.MatchString(r.Branch):
		return fmt.Errorf("o branch de uma correção precisa ser %s<nome>, não %q", BranchPrefix, r.Branch)
	case r.Branch == r.BaseBranch || r.BaseBranch == "":
		return errors.New("o PR precisa de um branch de destino diferente do branch da correção")
	case !shaRe.MatchString(r.BaseCommit):
		return fmt.Errorf("commit de base inválido: %q", r.BaseCommit)
	case len(r.Changes) == 0:
		return errors.New("a correção não altera nenhum arquivo")
	}
	for _, ch := range r.Changes {
		if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains("/"+ch.Path+"/", "/../") {
			return fmt.Errorf("caminho inválido na correção: %q", ch.Path)
		}
	}
	return nil
}

func (c *Client) api() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://api.github.com"
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "new-app-chaos")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("acessando o GitHub: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		hint := ""
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			hint = " (o PR_GITHUB_TOKEN foi recusado)"
		case http.StatusForbidden, http.StatusNotFound:
			hint = " (o PR_GITHUB_TOKEN tem contents:write e pull_requests:write neste repositório?)"
		}
		return &APIError{Status: resp.StatusCode, Message: e.Message + hint}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// APIError é uma resposta de erro do GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub respondeu %d: %s", e.Status, e.Message) }

// OpenDraftPR cria o branch com um commit sobre BaseCommit e abre o PR em rascunho.
func (c *Client) OpenDraftPR(ctx context.Context, r Request) (PR, error) {
	if !c.Configured() {
		return PR{}, ErrNotConfigured
	}
	if err := r.validate(); err != nil {
		return PR{}, err
	}
	repo := "/repos/" + r.Repo

	var base struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, repo+"/git/commits/"+r.BaseCommit, nil, &base); err != nil {
		return PR{}, fmt.Errorf("lendo o commit de base: %w", err)
	}
	// O modo de cada arquivo existente (ex.: executável) é mantido.
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, repo+"/git/trees/"+base.Tree.SHA+"?recursive=1", nil, &tree); err != nil {
		return PR{}, fmt.Errorf("lendo os arquivos do commit de base: %w", err)
	}
	modes := map[string]string{}
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			modes[e.Path] = e.Mode
		}
	}

	type entry struct {
		Path string  `json:"path"`
		Mode string  `json:"mode"`
		Type string  `json:"type"`
		SHA  *string `json:"sha"` // null apaga o arquivo
	}
	var entries []entry
	for _, ch := range r.Changes {
		mode := modes[ch.Path]
		if ch.Delete {
			if mode == "" {
				continue // já não existe na base
			}
			entries = append(entries, entry{Path: ch.Path, Mode: mode, Type: "blob"})
			continue
		}
		if mode == "" || mode == "120000" { // arquivo novo; um link simbólico nunca é reescrito como link
			mode = "100644"
		}
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := c.do(ctx, http.MethodPost, repo+"/git/blobs", map[string]string{"content": base64.StdEncoding.EncodeToString(ch.Content), "encoding": "base64"}, &blob); err != nil {
			return PR{}, fmt.Errorf("enviando %s: %w", ch.Path, err)
		}
		sha := blob.SHA
		entries = append(entries, entry{Path: ch.Path, Mode: mode, Type: "blob", SHA: &sha})
	}
	if len(entries) == 0 {
		return PR{}, errors.New("a correção não muda nada em relação ao commit de base")
	}
	var newTree, commit struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, repo+"/git/trees", map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, &newTree); err != nil {
		return PR{}, fmt.Errorf("montando os arquivos do commit: %w", err)
	}
	if err := c.do(ctx, http.MethodPost, repo+"/git/commits", map[string]any{"message": r.Message, "tree": newTree.SHA, "parents": []string{r.BaseCommit}}, &commit); err != nil {
		return PR{}, fmt.Errorf("criando o commit: %w", err)
	}
	// Criar a referência falha se o branch já existe: o app nunca reescreve um branch.
	if err := c.do(ctx, http.MethodPost, repo+"/git/refs", map[string]string{"ref": "refs/heads/" + r.Branch, "sha": commit.SHA}, nil); err != nil {
		return PR{}, fmt.Errorf("criando o branch %s: %w", r.Branch, err)
	}
	var pr struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, http.MethodPost, repo+"/pulls", map[string]any{"title": r.Title, "head": r.Branch, "base": r.BaseBranch, "body": r.Body, "draft": true}, &pr)
	if err != nil {
		return PR{Branch: r.Branch, Commit: commit.SHA}, fmt.Errorf("o branch %s foi criado, mas o PR não abriu: %w", r.Branch, err)
	}
	return PR{Number: pr.Number, URL: pr.HTMLURL, Branch: r.Branch, Commit: commit.SHA}, nil
}

// Comment escreve um comentário no PR (ex.: o resultado do experimento).
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	if !repoRe.MatchString(repo) || number <= 0 {
		return fmt.Errorf("PR inválido: %s#%d", repo, number)
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), map[string]string{"body": body}, nil)
}
