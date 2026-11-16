// Package patch confere um diff unificado antes de qualquer coisa ser feita com ele: se ele
// se aplica ao código (git apply --check) e se cabe nos limites de escopo de uma correção
// automática. As regras de escopo valem sobre o diff, e não sobre o prompt: o que o modelo
// promete não conta, só o que ele escreveu.
package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// File é o que um diff faz com um arquivo.
type File struct {
	Path    string `json:"path"` // caminho depois da mudança (o antigo, se o arquivo foi apagado)
	OldPath string `json:"oldPath,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	New     bool   `json:"new,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
}

// Parse lê os arquivos e a contagem de linhas de um diff unificado (formato do git ou do diff -u).
func Parse(diff string) ([]File, error) {
	var files []File
	var cur *File
	inHunk, sawOld := false, false // sawOld: o arquivo atual já teve a sua linha "---"
	flush := func() {
		if cur != nil && cur.Path != "" {
			files = append(files, *cur)
		}
		cur = nil
	}
	name := func(s string) string {
		s = strings.TrimSpace(s)
		if i := strings.IndexByte(s, '\t'); i >= 0 { // "arquivo\tdata" do diff -u
			s = s[:i]
		}
		if s == "/dev/null" {
			return ""
		}
		s = strings.Trim(s, `"`)
		if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
			s = s[2:]
		}
		return s
	}
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		// Num diff -u sem "diff --git", o próximo arquivo começa com "--- x" seguido de "+++ y",
		// que dentro de um bloco pareceriam linhas removida e acrescentada. O par decide: um
		// arquivo que escapasse daqui escaparia também da conferência de escopo.
		if inHunk && strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
			inHunk = false
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur, inHunk, sawOld = &File{}, false, false
			// "diff --git a/x b/x": o nome definitivo vem das linhas ---/+++ (ou rename to).
			if parts := strings.SplitN(strings.TrimPrefix(line, "diff --git "), " b/", 2); len(parts) == 2 {
				cur.Path = parts[1]
			}
		case strings.HasPrefix(line, "--- ") && !inHunk:
			if cur == nil || sawOld { // segundo "---" sem "diff --git" no meio: outro arquivo
				flush()
				cur = &File{}
			}
			sawOld = true
			cur.OldPath = name(line[4:])
			if cur.OldPath == "" {
				cur.New = true
			}
		case strings.HasPrefix(line, "+++ ") && !inHunk && cur != nil:
			if p := name(line[4:]); p != "" {
				cur.Path = p
			} else {
				cur.Deleted, cur.Path = true, cur.OldPath
			}
			if cur.OldPath == cur.Path {
				cur.OldPath = ""
			}
		case strings.HasPrefix(line, "new file mode") && cur != nil:
			cur.New = true
		case strings.HasPrefix(line, "deleted file mode") && cur != nil:
			cur.Deleted = true
		case strings.HasPrefix(line, "rename from ") && cur != nil:
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to ") && cur != nil:
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case (strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch")) && cur != nil:
			cur.Binary = true
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && cur != nil && strings.HasPrefix(line, "+"):
			cur.Added++
		case inHunk && cur != nil && strings.HasPrefix(line, "-"):
			cur.Removed++
		case inHunk && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, `\`) && line != "":
			inHunk = false
		}
	}
	flush()
	if len(files) == 0 {
		return nil, errors.New("o diff não altera nenhum arquivo (esperado: diff unificado com linhas ---, +++ e @@)")
	}
	for _, f := range files {
		for _, p := range []string{f.Path, f.OldPath} {
			if p != "" && (path.IsAbs(p) || p == ".." || strings.HasPrefix(path.Clean(p), "../")) {
				return nil, fmt.Errorf("o diff aponta para fora do repositório: %q", p)
			}
		}
	}
	return files, nil
}

// git roda o git em dir sem deixar ele achar um repositório acima (a cópia do código não tem .git).
func git(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir), "GIT_CONFIG_NOSYSTEM=1", "HOME="+os.TempDir())
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// ErrNoGit indica que o binário do git não está disponível para conferir o patch.
var ErrNoGit = errors.New("git não encontrado: o patch não pôde ser conferido")

// Check diz se o diff se aplica à pasta dir, sem alterar nada. O erro traz o motivo do git.
func Check(ctx context.Context, dir, diff string) error {
	return apply(ctx, dir, diff, "--check")
}

// Apply aplica o diff em dir.
func Apply(ctx context.Context, dir, diff string) error {
	return apply(ctx, dir, diff)
}

func apply(ctx context.Context, dir, diff string, extra ...string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrNoGit
	}
	if !strings.HasSuffix(diff, "\n") {
		diff += "\n"
	}
	// --recount: as contagens dos cabeçalhos @@ escritas por um modelo erram com frequência; o git
	// as refaz a partir do conteúdo. O contexto continua tendo que bater com o arquivo.
	args := append([]string{"apply", "--whitespace=nowarn", "--recount"}, extra...)
	if out, err := git(ctx, dir, diff, append(args, "-")...); err != nil {
		if out == "" {
			out = err.Error()
		}
		return fmt.Errorf("o patch não se aplica: %s", firstLines(out, 4))
	}
	return nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, " | ")
}
