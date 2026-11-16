package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// O histórico sobrevive à troca do ambiente: backup com o banco em uso, e restauração na subida.
func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, "old.db")
	s, err := Open(old)
	if err != nil {
		t.Fatal(err)
	}
	s.SaveScenario(ctx, scenario.Scenario{ID: "s1", Name: "do ambiente antigo"})
	copyPath := filepath.Join(dir, "backup.db")
	if err := Backup(old, copyPath); err != nil { // com o banco aberto
		t.Fatal(err)
	}
	s.Close()

	// Ambiente novo: banco vazio, e o backup chega como app.db.restore.
	fresh := filepath.Join(dir, "new", "app.db")
	os.MkdirAll(filepath.Dir(fresh), 0o755)
	n, _ := Open(fresh)
	n.SaveScenario(ctx, scenario.Scenario{ID: "vazio", Name: "do ambiente novo"})
	n.Close()
	if ok, err := ApplyRestore(fresh); ok || err != nil {
		t.Fatalf("sem arquivo de restauração nada acontece: %v %v", ok, err)
	}
	b, _ := os.ReadFile(copyPath)
	os.WriteFile(fresh+RestoreSuffix, b, 0o644)
	if ok, err := ApplyRestore(fresh); !ok || err != nil {
		t.Fatalf("restaurar: %v %v", ok, err)
	}
	n, err = Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if sc, err := n.GetScenario(ctx, "s1"); err != nil || sc.Name != "do ambiente antigo" {
		t.Fatalf("o histórico não voltou: %+v %v", sc, err)
	}
	if _, err := os.Stat(fresh + RestoreSuffix); err == nil {
		t.Fatal("o arquivo de restauração tem que sumir depois de aplicado")
	}
	n.Close()

	// Um arquivo que não é banco não apaga o que está lá.
	os.WriteFile(fresh+RestoreSuffix, []byte("lixo"), 0o644)
	if ok, err := ApplyRestore(fresh); ok || err == nil || !strings.Contains(err.Error(), "mantido") {
		t.Fatalf("arquivo corrompido: %v %v", ok, err)
	}
	n, _ = Open(fresh)
	defer n.Close()
	if _, err := n.GetScenario(ctx, "s1"); err != nil {
		t.Fatalf("o banco atual tinha que ser mantido: %v", err)
	}
}
