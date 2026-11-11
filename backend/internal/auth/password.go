// Package auth é o login do modo nuvem: usuários com senha, sessão por cookie e um
// middleware que fecha a API e o /grafana. Desligado (padrão local), tudo fica aberto.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Formato: pbkdf2-sha256$<iterações>$<sal>$<hash>, base64 sem padding.
const (
	hashPrefix = "pbkdf2-sha256"
	iterations = 210_000
	keyLen     = 32
)

var b64 = base64.RawStdEncoding

// HashPassword gera o hash guardado no banco (só biblioteca padrão, sem dependência nova).
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", hashPrefix, iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// IsHash diz se o texto já é um hash no formato acima (APP_USERS aceita senha ou hash).
func IsHash(s string) bool { return strings.HasPrefix(s, hashPrefix+"$") }

// CheckPassword compara em tempo constante.
func CheckPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
