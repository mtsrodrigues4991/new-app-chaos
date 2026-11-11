package coderepo

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxTreeFiles limita a árvore enviada no início da conversa.
const MaxTreeFiles = 400

// files lista os arquivos do snapshot (caminhos relativos, com /), em ordem.
func (b *Browser) files() []string {
	var out []string
	filepath.WalkDir(b.Snap.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(b.Snap.Root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// Tree devolve a lista de arquivos do repositório, um por linha. Em repositórios
// grandes, mostra só as pastas com a contagem de arquivos de cada uma.
func (b *Browser) Tree() string {
	files := b.files()
	if len(files) <= MaxTreeFiles {
		return strings.Join(files, "\n")
	}
	count := map[string]int{}
	var dirs []string
	for _, f := range files {
		d := path.Dir(f)
		if count[d] == 0 {
			dirs = append(dirs, d)
		}
		count[d]++
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "(%d arquivos; repositório grande, mostrando só as pastas; use list_dir para ver os arquivos)\n", len(files))
	for i, d := range dirs {
		if i >= MaxTreeFiles {
			fmt.Fprintf(&sb, "... e mais %d pastas\n", len(dirs)-i)
			break
		}
		name := d + "/"
		if d == "." {
			name = "(raiz)"
		}
		fmt.Fprintf(&sb, "%s (%d)\n", name, count[d])
	}
	return strings.TrimSpace(sb.String())
}

// equivalent agrupa extensões que costumam ser confundidas.
var equivalent = map[string][]string{
	".yml":        {".yaml", ".properties"},
	".yaml":       {".yml", ".properties"},
	".properties": {".yaml", ".yml"},
	".jsx":        {".tsx", ".js", ".ts"},
	".tsx":        {".jsx", ".ts", ".js"},
	".js":         {".ts", ".mjs", ".cjs"},
	".ts":         {".js", ".mts"},
}

// Locate confere um arquivo citado pela IA. Devolve o caminho que existe no
// repositório (o próprio, ou o equivalente mais provável: outra extensão parecida
// na mesma pasta, ou o mesmo nome em outra pasta quando só há um) e quantas linhas
// ele tem. ok=false quando nada parecido existe (por exemplo, um arquivo novo).
func (b *Browser) Locate(file string) (found string, lines int, ok bool) {
	_, rel, err := b.resolve(file)
	if err != nil || rel == "" {
		return "", 0, false
	}
	exists := func(r string) (int, bool) {
		full, _, err := b.resolve(r)
		if err != nil {
			return 0, false
		}
		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() {
			return 0, false
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return 0, false
		}
		return bytes.Count(data, []byte("\n")) + 1, true
	}
	if n, ok := exists(rel); ok {
		return rel, n, true
	}
	ext := path.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	for _, alt := range equivalent[strings.ToLower(ext)] {
		if n, ok := exists(base + alt); ok {
			return base + alt, n, true
		}
	}
	// Mesmo nome (ou equivalente) em outra pasta, só se for único.
	names := map[string]bool{path.Base(rel): true}
	for _, alt := range equivalent[strings.ToLower(ext)] {
		names[path.Base(base+alt)] = true
	}
	var cands []string
	for _, f := range b.files() {
		if names[path.Base(f)] {
			cands = append(cands, f)
		}
	}
	if len(cands) == 1 {
		n, _ := exists(cands[0])
		return cands[0], n, true
	}
	return "", 0, false
}
