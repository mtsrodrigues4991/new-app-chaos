package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// RestoreSuffix marca o arquivo que, posto ao lado do banco, o substitui na próxima subida.
const RestoreSuffix = ".restore"

// Backup grava em dst uma cópia consistente do banco em src, sem parar quem o está usando
// (VACUUM INTO: um retrato do banco num arquivo novo, já sem o WAL).
func Backup(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("banco %s: %w", src, err)
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	return err
}

// ApplyRestore troca o banco em path pelo arquivo path+RestoreSuffix, se ele existir e for um
// banco íntegro. É assim que o histórico volta depois de o ambiente ser recriado: o arquivo é
// copiado para o volume e o backend reinicia. Devolve true se trocou.
func ApplyRestore(path string) (bool, error) {
	from := path + RestoreSuffix
	if _, err := os.Stat(from); err != nil {
		return false, nil
	}
	db, err := sql.Open("sqlite", from+"?mode=ro")
	if err != nil {
		return false, err
	}
	var check string
	err = db.QueryRow(`PRAGMA quick_check`).Scan(&check)
	db.Close()
	if err != nil || check != "ok" {
		// Um arquivo corrompido não pode apagar o banco que está lá: fica de lado, com outro nome.
		_ = os.Rename(from, from+".invalido")
		return false, fmt.Errorf("o arquivo de restauração não é um banco íntegro (%v %s); o banco atual foi mantido", err, check)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	if err := os.Rename(from, path); err != nil {
		return false, err
	}
	return true, nil
}
