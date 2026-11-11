package coderepo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "dono-repo-abc/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "dono-repo-abc/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.WriteHeader(&tar.Header{Name: "dono-repo-abc/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func server(t *testing.T, files map[string]string, wantToken string) (*httptest.Server, *int) {
	downloads := 0
	tb := tarball(t, files)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(404)
			return
		}
		switch {
		case r.URL.Path == "/repos/dono/repo/commits/main":
			w.Write([]byte(sha))
		case r.URL.Path == "/repos/dono/repo/tarball/"+sha:
			downloads++
			w.Write(tb)
		default:
			w.WriteHeader(404)
		}
	}))
	return s, &downloads
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"dono/repo": "dono/repo", "https://github.com/dono/repo.git": "dono/repo", "github.com/dono/repo/": "dono/repo",
	} {
		if got, err := NormalizeRepo(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	if _, err := NormalizeRepo("só um nome"); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestFetchAndTools(t *testing.T) {
	files := map[string]string{
		"src/main/java/App.java":             "class App {\n  // GET /pix\n  void pix() {}\n}\n",
		"src/main/resources/application.yml": "spring:\n  datasource:\n    hikari:\n      maximum-pool-size: 5\n",
		"node_modules/lib/index.js":          "ignorado",
		"target/classes/App.class":           "ignorado",
	}
	s, downloads := server(t, files, "tok")
	defer s.Close()
	f := &Fetcher{Token: "tok", APIBase: s.URL, CacheDir: t.TempDir()}
	snap, err := f.Fetch(context.Background(), "https://github.com/dono/repo", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Commit != sha || snap.Repo != "dono/repo" {
		t.Fatalf("snapshot: %+v", snap)
	}
	if _, err := f.Fetch(context.Background(), "dono/repo", "main", ""); err != nil || *downloads != 1 {
		t.Fatalf("segunda busca deveria usar o cache (downloads=%d, err=%v)", *downloads, err)
	}
	if u := snap.URL("src/App.java", 3); u != "https://github.com/dono/repo/blob/"+sha+"/src/App.java#L3" {
		t.Fatalf("url: %s", u)
	}

	b := NewBrowser(snap)
	b.MaxReads = 1
	out, err := b.Call("list_dir", []byte(`{}`))
	if err != nil || !strings.Contains(out, "src/") || strings.Contains(out, "node_modules") || strings.Contains(out, "target") || strings.Contains(out, "link") {
		t.Fatalf("list_dir: %q %v", out, err)
	}
	out, err = b.Call("search_code", []byte(`{"pattern":"MAXIMUM-POOL","glob":"*.yml"}`))
	if err != nil || !strings.Contains(out, "src/main/resources/application.yml:4:") {
		t.Fatalf("search_code: %q %v", out, err)
	}
	out, err = b.Call("read_file", []byte(`{"path":"src/main/java/App.java","start_line":2,"end_line":3}`))
	if err != nil || !strings.Contains(out, "    2    // GET /pix") || strings.Contains(out, "class App") {
		t.Fatalf("read_file: %q %v", out, err)
	}
	if _, err := b.Call("read_file", []byte(`{"path":"src/main/java/App.java","start_line":1}`)); err != nil || len(b.Read) != 1 {
		t.Fatalf("reler o mesmo arquivo não deveria contar de novo: %v %v", err, b.Read)
	}
	if _, err := b.Call("read_file", []byte(`{"path":"src/main/resources/application.yml"}`)); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Fatalf("esperava limite de leituras: %v", err)
	}
	if _, err := b.Call("list_dir", []byte(`{"path":"../../.."}`)); err == nil {
		// "../../.." é limpo para a raiz; não pode sair dela.
		out, _ := b.ListDir("../../..")
		if !strings.Contains(out, "src/") {
			t.Fatalf("caminho com .. saiu da raiz: %q", out)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	s, _ := server(t, map[string]string{"a": "b"}, "tok")
	defer s.Close()
	f := &Fetcher{APIBase: s.URL, CacheDir: t.TempDir()}
	_, err := f.Fetch(context.Background(), "dono/repo", "main", "")
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("sem token num repo privado: %v", err)
	}
	f.Token = "tok"
	if _, err := f.Fetch(context.Background(), "dono/repo", "main", "nao/existe"); err == nil || !strings.Contains(err.Error(), "subpasta") {
		t.Fatalf("subpasta inexistente: %v", err)
	}
}

func TestTreeAndLocate(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"pom.xml", "src/main/resources/application.yaml", "src/main/java/App.java", "config/only.properties"} {
		full := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("l1\nl2\n"), 0o644)
	}
	b := NewBrowser(Snapshot{Repo: "d/r", Commit: "c", Root: root})
	tree := b.Tree()
	if !strings.Contains(tree, "src/main/resources/application.yaml") || !strings.Contains(tree, "pom.xml") {
		t.Fatalf("árvore: %q", tree)
	}
	cases := map[string]string{
		"pom.xml":                                   "pom.xml",
		"src/main/resources/application.yml":        "src/main/resources/application.yaml",
		"src/main/resources/application.properties": "src/main/resources/application.yaml",
		"only.properties":                           "config/only.properties",
		"Dockerfile":                                "",
		"../../etc/passwd":                          "",
	}
	for in, want := range cases {
		got, _, ok := b.Locate(in)
		if got != want || ok != (want != "") {
			t.Errorf("Locate(%q) = %q %v, queria %q", in, got, ok, want)
		}
	}
}
