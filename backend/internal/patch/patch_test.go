package patch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const poolDiff = `diff --git a/src/main/resources/application.yaml b/src/main/resources/application.yaml
index 1111111..2222222 100644
--- a/src/main/resources/application.yaml
+++ b/src/main/resources/application.yaml
@@ -1,4 +1,5 @@
 spring:
   datasource:
     hikari:
-      maximum-pool-size: 5
+      maximum-pool-size: 20
+      connection-timeout: 2000
`

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "src/main/resources/application.yaml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("spring:\n  datasource:\n    hikari:\n      maximum-pool-size: 5\n"), 0o644)
	return dir
}

func TestParse(t *testing.T) {
	fs, err := Parse(poolDiff)
	if err != nil || len(fs) != 1 {
		t.Fatalf("%v %+v", err, fs)
	}
	if f := fs[0]; f.Path != "src/main/resources/application.yaml" || f.Added != 2 || f.Removed != 1 || f.New || f.Deleted || f.OldPath != "" {
		t.Fatalf("arquivo: %+v", f)
	}
	multi := poolDiff + `diff --git a/Dockerfile b/Dockerfile
new file mode 100644
--- /dev/null
+++ b/Dockerfile
@@ -0,0 +1,2 @@
+FROM eclipse-temurin:17-jre
+USER app
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-tchau
`
	fs, err = Parse(multi)
	if err != nil || len(fs) != 3 || !fs[1].New || fs[1].Added != 2 || !fs[2].Deleted || fs[2].Path != "old.txt" || fs[2].Removed != 1 {
		t.Fatalf("vários arquivos: %v %+v", err, fs)
	}
	// Sem o cabeçalho "diff --git" (diff -u puro), como um modelo costuma escrever.
	plain := "--- a/App.java\n+++ b/App.java\n@@ -1 +1 @@\n-int pool = 5;\n+int pool = 20;\n"
	if fs, err := Parse(plain); err != nil || len(fs) != 1 || fs[0].Path != "App.java" || fs[0].Added != 1 {
		t.Fatalf("diff -u: %v %+v", err, fs)
	}
	if _, err := Parse("só um trecho de código, sem cabeçalho"); err == nil {
		t.Fatal("texto que não é diff tem que dar erro")
	}
	if _, err := Parse("--- a/x\n+++ b/../../etc/passwd\n@@ -1 +1 @@\n-a\n+b\n"); err == nil {
		t.Fatal("caminho para fora do repositório tem que dar erro")
	}
}

func TestCheckAndApply(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sem git")
	}
	ctx := context.Background()
	dir := repo(t)
	if err := Check(ctx, dir, poolDiff); err != nil {
		t.Fatalf("o patch deveria aplicar: %v", err)
	}
	// Check não altera nada.
	b, _ := os.ReadFile(filepath.Join(dir, "src/main/resources/application.yaml"))
	if !strings.Contains(string(b), "maximum-pool-size: 5") {
		t.Fatal("Check alterou o arquivo")
	}
	// O mesmo patch contra um arquivo diferente do que o modelo imaginou não aplica.
	wrong := strings.Replace(poolDiff, "maximum-pool-size: 5", "maximum-pool-size: 10", 1)
	if err := Check(ctx, dir, wrong); err == nil || !strings.Contains(err.Error(), "não se aplica") {
		t.Fatalf("patch com contexto errado: %v", err)
	}
	if err := Apply(ctx, dir, poolDiff); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "src/main/resources/application.yaml"))
	if !strings.Contains(string(b), "maximum-pool-size: 20") || !strings.Contains(string(b), "connection-timeout: 2000") {
		t.Fatalf("depois do Apply: %s", b)
	}
	if err := Check(ctx, dir, poolDiff); err == nil {
		t.Fatal("aplicar duas vezes não pode passar")
	}
}

// A cópia do código fica dentro da pasta de dados, que em desenvolvimento está dentro de um
// repositório git: o git não pode subir até ele e errar a raiz.
func TestCheckInsideAnotherRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sem git")
	}
	outer := t.TempDir()
	if out, err := exec.Command("git", "-C", outer, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	dir := filepath.Join(outer, "data", "code", "commit", "src")
	p := filepath.Join(dir, "src/main/resources/application.yaml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("spring:\n  datasource:\n    hikari:\n      maximum-pool-size: 5\n"), 0o644)
	if err := Check(context.Background(), dir, poolDiff); err != nil {
		t.Fatalf("dentro de outro repositório: %v", err)
	}
}

func review(t *testing.T, diff string) Review {
	t.Helper()
	fs, err := Parse(diff)
	if err != nil {
		t.Fatal(err)
	}
	return Guard(fs, DefaultLimits)
}

func change(path string) string {
	return "--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"
}

func TestGuard(t *testing.T) {
	if r := review(t, poolDiff); !r.OK() || r.Lines != 3 {
		t.Fatalf("correção pequena de configuração tem que passar: %+v", r)
	}
	for name, c := range map[string]struct{ diff, wants string }{
		"workflow de CI":   {change(".github/workflows/ci.yml"), "pipeline"},
		"segredo":          {change("config/.env.production"), "segredo"},
		"autenticação":     {change("src/main/java/app/security/SecurityConfig.java"), "autenticação"},
		"migração":         {change("src/main/resources/db/migration/V2__x.sql"), "migração"},
		"altera teste":     {change("src/test/java/app/PixTest.java"), "teste existente"},
		"apaga teste":      {"--- a/pix_test.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-func TestPix(t *testing.T) {}\n", "apaga um arquivo de teste"},
		"binário":          {"diff --git a/logo.png b/logo.png\nBinary files a/logo.png and b/logo.png differ\n", "binário"},
		"renomeia para CI": {"diff --git a/x.yml b/.github/workflows/x.yml\nrename from x.yml\nrename to .github/workflows/x.yml\n", "pipeline"},
	} {
		if r := review(t, c.diff); r.OK() || !strings.Contains(strings.Join(r.Violations, "\n"), c.wants) {
			t.Errorf("%s: esperava violação com %q: %+v", name, c.wants, r.Violations)
		}
	}
	// Acrescentar um teste novo, ou linhas a um teste, pode.
	add := "--- /dev/null\n+++ b/src/test/java/app/PoolTest.java\n@@ -0,0 +1,2 @@\n+class PoolTest {}\n+// novo\n"
	if r := review(t, add); !r.OK() {
		t.Errorf("teste novo deveria passar: %+v", r.Violations)
	}
	// Dependência não barra, mas é destacada para quem aprova.
	if r := review(t, change("pom.xml")); !r.OK() || len(r.Highlights) != 1 {
		t.Errorf("dependência: %+v", r)
	}
	// Tamanho.
	var many strings.Builder
	for i := 0; i < 6; i++ {
		many.WriteString(change("src/F" + string(rune('a'+i)) + ".java"))
	}
	if r := review(t, many.String()); r.OK() || !strings.Contains(r.Violations[0], "6 arquivos") {
		t.Errorf("6 arquivos: %+v", r.Violations)
	}
	big := "--- a/A.java\n+++ b/A.java\n@@ -1,0 +1,201 @@\n" + strings.Repeat("+linha\n", 201)
	if r := review(t, big); r.OK() || !strings.Contains(r.Violations[0], "201 linhas") {
		t.Errorf("201 linhas: %+v", r.Violations)
	}
}
