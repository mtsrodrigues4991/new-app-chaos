package ghpr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const baseSHA = "0123456789abcdef0123456789abcdef01234567"

// fakeGitHub registra o que foi pedido e responde como a API de dados do git.
type fakeGitHub struct {
	*httptest.Server
	calls []string
	posts map[string]map[string]any
	token string
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{posts: map[string]map[string]any{}, token: "tok"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &body)
			f.posts[r.URL.Path] = body
		}
		p := strings.TrimPrefix(r.URL.Path, "/repos/dono/repo")
		switch {
		case r.Method == "GET" && p == "/git/commits/"+baseSHA:
			w.Write([]byte(`{"tree":{"sha":"tree0"}}`))
		case r.Method == "GET" && p == "/git/trees/tree0":
			w.Write([]byte(`{"tree":[{"path":"mvnw","mode":"100755","type":"blob"},{"path":"src/App.java","mode":"100644","type":"blob"},{"path":"old.txt","mode":"100644","type":"blob"},{"path":"src","mode":"040000","type":"tree"}]}`))
		case r.Method == "POST" && p == "/git/blobs":
			w.Write([]byte(`{"sha":"blob-` + body["content"].(string)[:6] + `"}`))
		case r.Method == "POST" && p == "/git/trees":
			w.Write([]byte(`{"sha":"tree1"}`))
		case r.Method == "POST" && p == "/git/commits":
			w.Write([]byte(`{"sha":"commit1"}`))
		case r.Method == "POST" && p == "/git/refs":
			if body["ref"] == "refs/heads/nac/fix-existe" {
				w.WriteHeader(422)
				w.Write([]byte(`{"message":"Reference already exists"}`))
				return
			}
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		case r.Method == "POST" && p == "/pulls":
			w.WriteHeader(201)
			w.Write([]byte(`{"number":7,"html_url":"https://github.com/dono/repo/pull/7"}`))
		case r.Method == "POST" && p == "/issues/7/comments":
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func request() Request {
	return Request{Repo: "dono/repo", BaseBranch: "main", BaseCommit: baseSHA, Branch: "nac/fix-abc123", Title: "Aumenta o pool", Body: "evidência", Message: "Aumenta o pool de conexões",
		Changes: []Change{{Path: "src/App.java", Content: []byte("class App {}\n")}, {Path: "mvnw", Content: []byte("#!/bin/sh\n")}, {Path: "Novo.java", Content: []byte("class Novo {}\n")}, {Path: "old.txt", Delete: true}, {Path: "nunca-existiu.txt", Delete: true}}}
}

func TestOpenDraftPR(t *testing.T) {
	f := newFake(t)
	c := &Client{Token: "tok", APIBase: f.URL}
	pr, err := c.OpenDraftPR(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.URL != "https://github.com/dono/repo/pull/7" || pr.Branch != "nac/fix-abc123" || pr.Commit != "commit1" {
		t.Fatalf("PR: %+v", pr)
	}
	// Sempre rascunho, do branch nac/ para o branch de destino.
	pull := f.posts["/repos/dono/repo/pulls"]
	if pull["draft"] != true || pull["head"] != "nac/fix-abc123" || pull["base"] != "main" {
		t.Fatalf("pedido do PR: %+v", pull)
	}
	// O commit tem como pai o commit que rodou no teste.
	if parents := f.posts["/repos/dono/repo/git/commits"]["parents"].([]any); len(parents) != 1 || parents[0] != baseSHA {
		t.Fatalf("pais do commit: %v", parents)
	}
	// Árvore: modos preservados (mvnw segue executável), arquivo novo 100644, remoção com sha nulo,
	// e a remoção de um arquivo que não existe na base fica de fora.
	entries := f.posts["/repos/dono/repo/git/trees"]["tree"].([]any)
	got := map[string]map[string]any{}
	for _, e := range entries {
		m := e.(map[string]any)
		got[m["path"].(string)] = m
	}
	if len(got) != 4 || got["mvnw"]["mode"] != "100755" || got["src/App.java"]["mode"] != "100644" || got["Novo.java"]["mode"] != "100644" {
		t.Fatalf("árvore: %+v", got)
	}
	if sha, present := got["old.txt"]["sha"]; !present || sha != nil {
		t.Fatalf("a remoção vai com sha nulo: %+v", got["old.txt"])
	}
	if content := f.posts["/repos/dono/repo/git/blobs"]["content"].(string); content == "" {
		t.Fatal("blob sem conteúdo")
	} else if _, err := base64.StdEncoding.DecodeString(content); err != nil {
		t.Fatalf("blob não é base64: %v", err)
	}
	if err := c.Comment(context.Background(), "dono/repo", 7, "resultado"); err != nil {
		t.Fatal(err)
	}
}

// As travas que não dependem de quem chama: nada é enviado ao GitHub nesses casos.
func TestRefusals(t *testing.T) {
	f := newFake(t)
	c := &Client{Token: "tok", APIBase: f.URL}
	for name, change := range map[string]func(*Request){
		"branch fora de nac/":      func(r *Request) { r.Branch = "main" },
		"branch igual ao destino":  func(r *Request) { r.Branch, r.BaseBranch = "nac/x", "nac/x" },
		"branch com barra a mais":  func(r *Request) { r.Branch = "nac/../main" },
		"sem mudanças":             func(r *Request) { r.Changes = nil },
		"commit de base abreviado": func(r *Request) { r.BaseCommit = "0123456" },
		"caminho para fora":        func(r *Request) { r.Changes = []Change{{Path: "../x", Content: []byte("x")}} },
		"repositório inválido":     func(r *Request) { r.Repo = "dono/repo/../outro" },
	} {
		r := request()
		change(&r)
		if _, err := c.OpenDraftPR(context.Background(), r); err == nil {
			t.Errorf("%s: esperava recusa", name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("nenhuma chamada deveria ter saído: %v", f.calls)
	}
	if _, err := (&Client{APIBase: f.URL}).OpenDraftPR(context.Background(), request()); err != ErrNotConfigured {
		t.Fatalf("sem token: %v", err)
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t)
	// Branch que já existe: o app não reescreve, e nenhum PR é aberto.
	r := request()
	r.Branch = "nac/fix-existe"
	_, err := (&Client{Token: "tok", APIBase: f.URL}).OpenDraftPR(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "criando o branch") {
		t.Fatalf("branch existente: %v", err)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/pulls") {
			t.Fatal("não pode abrir PR se o branch não foi criado")
		}
	}
	// Token recusado: a mensagem diz qual token conferir.
	_, err = (&Client{Token: "errado", APIBase: f.URL}).OpenDraftPR(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "PR_GITHUB_TOKEN") {
		t.Fatalf("token recusado: %v", err)
	}
}
