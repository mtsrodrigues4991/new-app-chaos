package patch

import (
	"fmt"
	"path"
	"strings"
)

// Limits são os limites de escopo de uma correção automática.
type Limits struct {
	MaxFiles int // arquivos alterados
	MaxLines int // linhas acrescentadas + removidas
}

// DefaultLimits é o teto sugerido: uma correção, não uma refatoração.
var DefaultLimits = Limits{MaxFiles: 5, MaxLines: 200}

// Review é o parecer sobre um diff.
type Review struct {
	Files []File `json:"files"`
	Lines int    `json:"lines"`
	// Violations impedem a correção de seguir (vazio = dentro do escopo).
	Violations []string `json:"violations"`
	// Highlights não impedem, mas têm que aparecer para quem aprova e no PR.
	Highlights []string `json:"highlights"`
}

// OK diz se o diff está dentro do escopo.
func (r Review) OK() bool { return len(r.Violations) == 0 }

func has(p string, parts ...string) bool {
	for _, s := range parts {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// forbidden devolve por que um caminho não pode ser tocado por uma correção automática ("" = pode).
func forbidden(p string) string {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	switch {
	case strings.HasPrefix(lower, ".github/") || has(lower, ".gitlab-ci", "jenkinsfile", ".circleci/", "azure-pipelines"):
		return "pipeline de CI/CD"
	case base == ".env" || strings.HasPrefix(base, ".env.") || has(base, "secret", "credential", "id_rsa", ".pem", ".key", ".p12", ".jks", ".pfx") || base == ".npmrc" || base == ".netrc":
		return "arquivo de segredo"
	case has(lower, "/auth/", "/security/", "/login/") || strings.HasPrefix(lower, "auth/") || strings.HasPrefix(lower, "security/") || has(base, "securityconfig", "authfilter", "jwt"):
		return "código de autenticação"
	case has(lower, "/migration/", "/migrations/", "db/migrate", "/flyway/", "/liquibase/", "/changelog/") || strings.HasPrefix(lower, "migrations/"):
		return "migração de banco"
	}
	return ""
}

// IsTest diz se o caminho é de teste.
func IsTest(p string) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	return has(lower, "/test/", "/tests/", "/__tests__/", "/spec/") || strings.HasPrefix(lower, "test/") || strings.HasPrefix(lower, "tests/") ||
		strings.HasSuffix(base, "_test.go") || has(base, "test.java", "tests.java", "test.kt", ".test.", ".spec.", "_test.py") || strings.HasPrefix(base, "test_")
}

// dependencyFiles são os arquivos que declaram dependências.
var dependencyFiles = map[string]bool{
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true, "package.json": true, "package-lock.json": true,
	"go.mod": true, "go.sum": true, "requirements.txt": true, "pyproject.toml": true, "cargo.toml": true, "gemfile": true,
}

// Guard confere o diff contra os limites de escopo.
func Guard(files []File, lim Limits) Review {
	r := Review{Files: files, Violations: []string{}, Highlights: []string{}}
	for _, f := range files {
		r.Lines += f.Added + f.Removed
		for _, p := range []string{f.Path, f.OldPath} {
			if p == "" {
				continue
			}
			if why := forbidden(p); why != "" {
				r.Violations = append(r.Violations, fmt.Sprintf("%s: %s não pode ser alterado por uma correção automática", p, why))
				break
			}
		}
		if f.Binary {
			r.Violations = append(r.Violations, fmt.Sprintf("%s: arquivo binário", f.Path))
		}
		// Apagar ou afrouxar testes para o build passar é o atalho que a regra existe para barrar:
		// em arquivo de teste só se acrescenta.
		if IsTest(f.Path) || (f.OldPath != "" && IsTest(f.OldPath)) {
			switch {
			case f.Deleted:
				r.Violations = append(r.Violations, fmt.Sprintf("%s: apaga um arquivo de teste", f.Path))
			case f.Removed > 0:
				r.Violations = append(r.Violations, fmt.Sprintf("%s: remove ou altera %d linha(s) de um teste existente", f.Path, f.Removed))
			}
		}
		if dependencyFiles[strings.ToLower(path.Base(f.Path))] {
			r.Highlights = append(r.Highlights, fmt.Sprintf("%s: muda dependências; confira se a versão está fixa", f.Path))
		}
	}
	if len(files) > lim.MaxFiles {
		r.Violations = append(r.Violations, fmt.Sprintf("altera %d arquivos (o teto é %d)", len(files), lim.MaxFiles))
	}
	if r.Lines > lim.MaxLines {
		r.Violations = append(r.Violations, fmt.Sprintf("altera %d linhas (o teto é %d)", r.Lines, lim.MaxLines))
	}
	return r
}
