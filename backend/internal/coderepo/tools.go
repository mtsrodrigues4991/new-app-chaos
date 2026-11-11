package coderepo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Limites das ferramentas, para a análise não sair cara nem lenta.
const (
	DefaultMaxReads = 30  // arquivos abertos por análise ou pergunta
	MaxCalls        = 80  // chamadas de ferramenta no total
	MaxReadLines    = 300 // linhas por leitura
	MaxListEntries  = 300
	MaxMatches      = 80
	maxLineChars    = 240
)

// Browser executa as ferramentas sobre uma cópia local do código.
type Browser struct {
	Snap     Snapshot
	MaxReads int

	mu    sync.Mutex
	calls int
	Read  []string // arquivos abertos, na ordem
}

func NewBrowser(s Snapshot) *Browser { return &Browser{Snap: s, MaxReads: DefaultMaxReads} }

// resolve transforma um caminho relativo à raiz em caminho local, sem sair da raiz.
func (b *Browser) resolve(p string) (string, string, error) {
	rel := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
	full := filepath.Join(b.Snap.Root, filepath.FromSlash(rel))
	if full != b.Snap.Root && !strings.HasPrefix(full, b.Snap.Root+string(filepath.Separator)) {
		return "", "", errors.New("caminho fora do repositório")
	}
	return full, rel, nil
}

// count registra uma chamada. file != "" é a abertura de um arquivo: só arquivos
// ainda não abertos contam para o limite (ler outro trecho do mesmo é livre).
func (b *Browser) count(file string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls > MaxCalls {
		return errors.New("limite de chamadas de ferramenta atingido; responda agora com o que já tem")
	}
	if file != "" && !slices.Contains(b.Read, file) {
		if len(b.Read) >= b.MaxReads {
			return fmt.Errorf("limite de %d arquivos abertos atingido; responda agora com o que já tem", b.MaxReads)
		}
		b.Read = append(b.Read, file)
	}
	return nil
}

// ListDir lista uma pasta (pastas terminam com "/").
func (b *Browser) ListDir(p string) (string, error) {
	if err := b.count(""); err != nil {
		return "", err
	}
	full, rel, err := b.resolve(p)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", fmt.Errorf("pasta %q não encontrada", rel)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name()+"/")
		} else if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	more := ""
	if len(out) > MaxListEntries {
		more = fmt.Sprintf("\n... e mais %d itens", len(out)-MaxListEntries)
		out = out[:MaxListEntries]
	}
	if rel == "" {
		rel = "."
	}
	return fmt.Sprintf("%s:\n%s%s", rel, strings.Join(out, "\n"), more), nil
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

func clip(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > maxLineChars {
		return s[:maxLineChars] + "…"
	}
	return s
}

// Search procura uma expressão regular (sem diferenciar maiúsculas) nos arquivos de texto.
// dir limita a busca a uma pasta; glob filtra pelo nome do arquivo (ex.: *.java).
func (b *Browser) Search(pattern, dir, glob string) (string, error) {
	if err := b.count(""); err != nil {
		return "", err
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return "", fmt.Errorf("expressão inválida: %v", err)
	}
	full, _, err := b.resolve(dir)
	if err != nil {
		return "", err
	}
	var hits []string
	truncated := false
	err = filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if err != nil || truncated {
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		data, err := os.ReadFile(p)
		if err != nil || isBinary(data) {
			return nil
		}
		rel, _ := filepath.Rel(b.Snap.Root, p)
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), MaxFileBytes)
		for n := 1; sc.Scan(); n++ {
			if re.MatchString(sc.Text()) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), n, strings.TrimSpace(clip(sc.Text()))))
				if len(hits) >= MaxMatches {
					truncated = true
					return nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "nenhum resultado", nil
	}
	out := strings.Join(hits, "\n")
	if truncated {
		out += fmt.Sprintf("\n... parei em %d resultados; refine a busca", MaxMatches)
	}
	return out, nil
}

// ReadFile devolve as linhas [start, end] numeradas (até MaxReadLines).
func (b *Browser) ReadFile(p string, start, end int) (string, error) {
	full, rel, err := b.resolve(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(full)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("arquivo %q não encontrado", rel)
	}
	if err := b.count(rel); err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", fmt.Errorf("%q é um arquivo binário", rel)
	}
	lines := strings.Split(string(data), "\n")
	if start < 1 {
		start = 1
	}
	if end < start || end-start+1 > MaxReadLines {
		end = start + MaxReadLines - 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (linhas %d a %d de %d)\n", rel, start, end, len(lines))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&sb, "%5d  %s\n", i, clip(lines[i-1]))
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... continua (peça start_line=%d)\n", end+1)
	}
	return sb.String(), nil
}

// Call executa uma ferramenta pelo nome, com a entrada em JSON.
func (b *Browser) Call(name string, input json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		Pattern   string `json:"pattern"`
		Glob      string `json:"glob"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("entrada inválida: %v", err)
		}
	}
	switch name {
	case "list_dir":
		return b.ListDir(in.Path)
	case "search_code":
		if strings.TrimSpace(in.Pattern) == "" {
			return "", errors.New("informe pattern")
		}
		return b.Search(in.Pattern, in.Path, in.Glob)
	case "read_file":
		if strings.TrimSpace(in.Path) == "" {
			return "", errors.New("informe path")
		}
		return b.ReadFile(in.Path, in.StartLine, in.EndLine)
	}
	return "", fmt.Errorf("ferramenta desconhecida: %s", name)
}

// ToolSpec descreve uma ferramenta para o modelo.
type ToolSpec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// Specs são as ferramentas oferecidas à IA.
func (b *Browser) Specs() []ToolSpec {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	num := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
	return []ToolSpec{
		{Name: "list_dir", Description: "Lista arquivos e pastas de uma pasta do repositório da aplicação testada (pastas terminam com /). Use path vazio para a raiz.",
			Properties: map[string]any{"path": str("Pasta relativa à raiz do repositório, ex.: src/main/resources")}},
		{Name: "search_code", Description: fmt.Sprintf("Busca uma expressão regular (sem diferenciar maiúsculas) no conteúdo dos arquivos de texto do repositório e devolve arquivo:linha: trecho (até %d resultados). Bom para achar o controller de um endpoint, configurações de pool, timeout, retry e afins.", MaxMatches),
			Properties: map[string]any{
				"pattern": str("Expressão regular, ex.: maximumPoolSize|connectionTimeout"),
				"path":    str("Pasta onde buscar (opcional; vazio = repositório todo)"),
				"glob":    str("Filtro pelo nome do arquivo (opcional), ex.: *.java ou application*.yml"),
			}, Required: []string{"pattern"}},
		{Name: "read_file", Description: fmt.Sprintf("Abre um arquivo e devolve as linhas numeradas (até %d por vez). Cada análise pode abrir no máximo %d arquivos, então escolha bem.", MaxReadLines, b.MaxReads),
			Properties: map[string]any{
				"path":       str("Arquivo relativo à raiz do repositório"),
				"start_line": num("Primeira linha (opcional, padrão 1)"),
				"end_line":   num("Última linha (opcional)"),
			}, Required: []string{"path"}},
	}
}
