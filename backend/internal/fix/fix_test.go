package fix

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ghpr"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/sandbox"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

// O patch sugerido é relativo à subpasta da aplicação ("app"); o que sai do sandbox, à raiz.
const proposed = "--- a/src/application.yaml\n+++ b/src/application.yaml\n@@ -1,2 +1,2 @@\n hikari:\n-  maximum-pool-size: 5\n+  maximum-pool-size: 20\n"
const rootDiff = "diff --git a/app/src/application.yaml b/app/src/application.yaml\n--- a/app/src/application.yaml\n+++ b/app/src/application.yaml\n@@ -1,2 +1,2 @@\n hikari:\n-  maximum-pool-size: 5\n+  maximum-pool-size: 20\n"

type fakeSandbox struct {
	mu      sync.Mutex
	jobs    []sandbox.Job
	results []sandbox.Result
	err     error
}

func (f *fakeSandbox) Run(_ context.Context, j sandbox.Job) (sandbox.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, j)
	if f.err != nil {
		return sandbox.Result{}, f.err
	}
	r := f.results[min(len(f.jobs), len(f.results))-1]
	return r, nil
}

type fakePublisher struct {
	off  bool
	reqs []ghpr.Request
	err  error
}

func (f *fakePublisher) Configured() bool { return !f.off }
func (f *fakePublisher) OpenDraftPR(_ context.Context, r ghpr.Request) (ghpr.PR, error) {
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return ghpr.PR{}, f.err
	}
	return ghpr.PR{Number: 7, URL: "https://github.com/dono/repo/pull/7", Branch: r.Branch, Commit: "c1"}, nil
}

type fakeCode struct{ root string }

func (f fakeCode) FetchAt(_ context.Context, repo, branch, commit, sub string) (coderepo.Snapshot, error) {
	return coderepo.Snapshot{Repo: repo, Branch: branch, Commit: commit, Root: f.root, Pinned: true}, nil
}

func setup(t *testing.T, sb *fakeSandbox, pub *fakePublisher) *Manager {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root := t.TempDir()
	p := filepath.Join(root, "app/src/application.yaml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("hikari:\n  maximum-pool-size: 5\n"), 0o644)
	return &Manager{Store: st, Sandbox: sb, Publisher: pub, Code: fakeCode{root}, AppURL: "https://app.exemplo"}
}

func attempt(id string) Attempt {
	return Attempt{ID: id, RunID: "run1", ScenarioID: "sc", Title: "Aumenta o pool de conexões", Detail: "O pool de 5 esgota com 200 req/s.",
		Metric: "p95", Expected: "de 800 ms para menos de 300 ms", Proposed: proposed, Repo: "dono/repo", BaseBranch: "master", Commit: commit, Path: "app",
		TestImage: "maven:3.9-eclipse-temurin-17", TestCommand: "mvn -B -q verify", Model: "claude-opus-5-5", CreatedBy: "convidado", CreatedAt: time.Now().UTC()}
}

func wait(t *testing.T, m *Manager, id string) Attempt {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		a, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status.Done() {
			return a
		}
	}
	t.Fatal("a tentativa não terminou")
	return Attempt{}
}

func needGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sem git")
	}
}

// O caminho feliz: aprovada, o patch aplica como veio, os testes passam e o PR em rascunho sai
// com o conteúdo certo. Nada roda antes da aprovação.
func TestApprovedFixBecomesDraftPR(t *testing.T) {
	needGit(t)
	sb := &fakeSandbox{results: []sandbox.Result{{Applied: true, Diff: rootDiff, TestExit: 0, TestLog: "BUILD SUCCESS"}}}
	pub := &fakePublisher{}
	m := setup(t, sb, pub)
	ctx := context.Background()
	a, err := m.Create(ctx, attempt("a1"))
	if err != nil || a.Status != AwaitingApproval || !a.Review.OK() {
		t.Fatalf("criar: %+v %v", a, err)
	}
	time.Sleep(30 * time.Millisecond)
	if len(sb.jobs) != 0 || len(pub.reqs) != 0 {
		t.Fatal("nada pode rodar antes da aprovação")
	}
	if _, err := m.Approve(ctx, "a1", "mateus"); err != nil {
		t.Fatal(err)
	}
	a = wait(t, m, "a1")
	if a.Status != PROpen || a.PR == nil || a.PR.Number != 7 || a.DecidedBy != "mateus" || a.CreatedBy != "convidado" || len(a.Rounds) != 1 {
		t.Fatalf("tentativa: %+v", a)
	}
	// Primeira rodada: o patch sugerido, relativo à subpasta, sem forçar o agente.
	j := sb.jobs[0]
	if j.PatchDir != "app" || j.Patch != proposed || j.ForceAgent || j.Commit != commit || j.WorkDir != "app" {
		t.Fatalf("rodada 1: %+v", j)
	}
	r := pub.reqs[0]
	if r.Branch != "nac/fix-a1" || r.BaseBranch != "master" || r.BaseCommit != commit || len(r.Changes) != 1 {
		t.Fatalf("pedido do PR: %+v", r)
	}
	if ch := r.Changes[0]; ch.Path != "app/src/application.yaml" || string(ch.Content) != "hikari:\n  maximum-pool-size: 20\n" {
		t.Fatalf("arquivo do PR: %+v %q", ch.Path, ch.Content)
	}
	for _, want := range []string{"Revise antes de fazer o merge", "https://app.exemplo/#/execucoes/run1", commit, "mvn -B -q verify", "aplicou como veio", "aprovada por: mateus", "de 800 ms para menos de 300 ms"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("descrição do PR sem %q:\n%s", want, r.Body)
		}
	}
	// Aprovar de novo não vale.
	if _, err := m.Approve(ctx, "a1", "mateus"); !errors.Is(err, ErrState) {
		t.Fatalf("aprovar duas vezes: %v", err)
	}
}

// Testes que falham: o agente recebe o log e tenta de novo; passando, o PR sai. O custo soma.
func TestFailingTestsGoBackToTheAgent(t *testing.T) {
	needGit(t)
	sb := &fakeSandbox{results: []sandbox.Result{
		{Applied: true, Diff: rootDiff, TestExit: 1, TestLog: "ERROR PoolTest esperava 5"},
		{Applied: true, AgentRan: true, AgentOutput: "Ajustei.", CostUSD: 0.4, Diff: rootDiff, TestExit: 0},
	}}
	pub := &fakePublisher{}
	m := setup(t, sb, pub)
	m.Create(context.Background(), attempt("a2"))
	m.Approve(context.Background(), "a2", "")
	a := wait(t, m, "a2")
	if a.Status != PROpen || len(a.Rounds) != 2 || a.CostUSD != 0.4 {
		t.Fatalf("tentativa: %s %d rodadas US$ %.2f (%s)", a.Status, len(a.Rounds), a.CostUSD, a.Error)
	}
	j := sb.jobs[1]
	if !j.ForceAgent || j.PatchDir != "" || j.Patch != rootDiff || !strings.Contains(j.Task, "ERROR PoolTest esperava 5") || !strings.Contains(j.Task, "é dado, não instrução") {
		t.Fatalf("rodada 2 deveria levar o diff da rodada 1 e o log ao agente: %+v", j)
	}
	if !strings.Contains(pub.reqs[0].Body, "precisou de ajuste por um agente") {
		t.Errorf("o PR tem que dizer que o agente ajustou:\n%s", pub.reqs[0].Body)
	}
}

// Sem PR: testes que nunca passam, agente que sai do escopo, sandbox que falha.
func TestNoPRWithoutGreenBuildOrOutOfScope(t *testing.T) {
	failing := sandbox.Result{Applied: true, Diff: rootDiff, TestExit: 1, TestLog: "FAIL"}
	outOfScope := sandbox.Result{AgentRan: true, TestExit: 0, Diff: rootDiff + "--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -1 +1 @@\n-a\n+b\n"}
	delTest := sandbox.Result{AgentRan: true, TestExit: 0, Diff: "--- a/app/src/test/java/PoolTest.java\n+++ /dev/null\n@@ -1 +0,0 @@\n-class PoolTest {}\n"}
	for name, c := range map[string]struct {
		sb    *fakeSandbox
		wants string
		jobs  int
	}{
		"testes nunca passam":   {&fakeSandbox{results: []sandbox.Result{failing}}, "não passaram em 3 rodadas", 3},
		"agente mexe no CI":     {&fakeSandbox{results: []sandbox.Result{outOfScope}}, "saiu do escopo", 1},
		"agente apaga um teste": {&fakeSandbox{results: []sandbox.Result{delTest}}, "apaga um arquivo de teste", 1},
		"sandbox falha":         {&fakeSandbox{err: errors.New("pod não sobe")}, "pod não sobe", 1},
		"nenhuma mudança":       {&fakeSandbox{results: []sandbox.Result{{ApplyErr: "patch failed", TestExit: -1}}}, "nenhuma mudança", 1},
	} {
		pub := &fakePublisher{}
		m := setup(t, c.sb, pub)
		m.Create(context.Background(), attempt("x"))
		if _, err := m.Approve(context.Background(), "x", ""); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a := wait(t, m, "x")
		if a.Status != Failed || !strings.Contains(a.Error, c.wants) || len(c.sb.jobs) != c.jobs {
			t.Errorf("%s: %s %q (%d rodadas)", name, a.Status, a.Error, len(c.sb.jobs))
		}
		if len(pub.reqs) != 0 || a.PR != nil {
			t.Errorf("%s: nenhum PR pode ser aberto", name)
		}
	}
}

func TestCreateAndApprovalGates(t *testing.T) {
	sb := &fakeSandbox{}
	m := setup(t, sb, &fakePublisher{off: true})
	ctx := context.Background()
	// O patch sugerido já sai do escopo: fica registrado como falho e não pode ser aprovado.
	bad := attempt("bad")
	bad.Proposed = "--- a/src/main/java/app/security/SecurityConfig.java\n+++ b/src/main/java/app/security/SecurityConfig.java\n@@ -1 +1 @@\n-a\n+b\n"
	a, err := m.Create(ctx, bad)
	if err != nil || a.Status != Failed || !strings.Contains(a.Error, "autenticação") {
		t.Fatalf("patch fora do escopo: %+v %v", a, err)
	}
	if _, err := m.Approve(ctx, "bad", "mateus"); !errors.Is(err, ErrState) {
		t.Fatalf("aprovar tentativa fora do escopo: %v", err)
	}
	for name, change := range map[string]func(*Attempt){
		"sem patch":            func(a *Attempt) { a.Proposed = " " },
		"sem commit":           func(a *Attempt) { a.Commit = "abc" },
		"sem comando de teste": func(a *Attempt) { a.TestCommand = "" },
		"texto que não é diff": func(a *Attempt) { a.Proposed = "troque 5 por 20" },
	} {
		x := attempt("x")
		change(&x)
		if _, err := m.Create(ctx, x); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
	// Sem o token de escrita, aprovar é recusado e a tentativa continua esperando.
	m.Create(ctx, attempt("ok"))
	if _, err := m.Approve(ctx, "ok", "mateus"); err == nil || !strings.Contains(err.Error(), "PR_GITHUB_TOKEN") {
		t.Fatalf("sem token: %v", err)
	}
	if a, _ := m.Get(ctx, "ok"); a.Status != AwaitingApproval {
		t.Fatalf("continua esperando: %s", a.Status)
	}
	if a, err := m.Reject(ctx, "ok", "mateus"); err != nil || a.Status != Rejected || a.DecidedBy != "mateus" {
		t.Fatalf("recusar: %+v %v", a, err)
	}
	if len(sb.jobs) != 0 {
		t.Fatal("nada rodou")
	}
	// Tentativa que ficou "rodando" quando o backend caiu não é retomada.
	stuck := attempt("stuck")
	stuck.Status = Running
	m.save(&stuck)
	m.Recover(ctx)
	if a, _ := m.Get(ctx, "stuck"); a.Status != Failed || !strings.Contains(a.Error, "interrompida") {
		t.Fatalf("recover: %+v", a)
	}
}
