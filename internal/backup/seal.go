package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	formatName   = "magpie-backup"
	kdfName      = "pbkdf2-sha256"
	curVersion   = 1
	defaultIter  = 600000
	minIter      = 100000
	maxIter      = 10000000
	saltLen      = 16
	nonceLen     = 12
	maxBackupLen = 256 << 20
)

var (
	// ErrPassphrase is returned by Open when the passphrase is wrong or the
	// file was changed: AES-GCM cannot tell the two apart.
	ErrPassphrase = errors.New("passphrase wrong or file modified / 口令错误或文件已被改动")
	// ErrNotBackup: the data is not a magpie backup at all.
	ErrNotBackup = errors.New("不是 magpie 备份")
	// ErrTooNew: the backup was made by a newer magpie.
	ErrTooNew = errors.New("备份由更新版本的 magpie 生成,请升级 magpie")
)

type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

func (h header) line() []byte {
	return []byte(fmt.Sprintf(`{"format": %q, "version": %d, "kdf": %q, "iterations": %d, "salt": %q, "nonce": %q}`,
		h.Format, h.Version, h.KDF, h.Iterations, h.Salt, h.Nonce))
}

func gcm(pass string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Seal encrypts a bundle with a passphrase. The file is one plain JSON header
// line, a newline, then the base64 of the AES-256-GCM ciphertext; the header's
// exact bytes are the cipher's associated data, so changing any of it is
// caught.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the passphrase is empty")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	h := header{Format: formatName, Version: curVersion, KDF: kdfName, Iterations: defaultIter,
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce)}
	aead, err := gcm(pass, salt, h.Iterations)
	if err != nil {
		return nil, err
	}
	head := h.line()
	ct := aead.Seal(nil, nonce, plain, head)
	out := append([]byte{}, head...)
	out = append(out, '\n')
	out = append(out, base64.StdEncoding.EncodeToString(ct)...)
	out = append(out, '\n')
	return out, nil
}

// Open decrypts a backup. The format is judged first, then the version, then
// the passphrase.
func Open(data []byte, pass string) (Bundle, error) {
	var zero Bundle
	head, body, _ := bytes.Cut(data, []byte("\n"))
	head = bytes.TrimRight(head, "\r")
	var h header
	if json.Unmarshal(head, &h) != nil || h.Format != formatName {
		return zero, ErrNotBackup
	}
	if h.Version > curVersion {
		return zero, ErrTooNew
	}
	if h.KDF != kdfName || h.Iterations < minIter || h.Iterations > maxIter {
		return zero, ErrPassphrase
	}
	salt, err1 := base64.StdEncoding.DecodeString(h.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(h.Nonce)
	ct, err3 := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body)))
	if err1 != nil || err2 != nil || err3 != nil || len(salt) < 8 || len(nonce) != nonceLen {
		return zero, ErrPassphrase
	}
	aead, err := gcm(pass, salt, h.Iterations)
	if err != nil {
		return zero, ErrPassphrase
	}
	plain, err := aead.Open(nil, nonce, ct, head)
	if err != nil {
		return zero, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return zero, fmt.Errorf("the backup is damaged: %w", err)
	}
	if b.Version > curVersion {
		return zero, ErrTooNew
	}
	return b, nil
}
