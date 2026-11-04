package backup

// The file is one JSON object: a plaintext header (format, version, kdf,
// iterations, salt, nonce) and "data", the base64 AES-256-GCM ciphertext of
// the whole Bundle. The header, re-encoded canonically without "data", is the
// additional authenticated data, so changing any header field — the
// iteration count included — fails the authentication in Open.

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
	"io"
)

const (
	// Format is the header's format value; a file without it is not a backup.
	Format = "magpie-backup"
	// CurrentVersion is the newest file version this build can open.
	CurrentVersion = 1
	// KDF is the key derivation the file names.
	KDF = "pbkdf2-sha256"

	defaultIterations = 600000
	// maxIterations bounds the work Open will do for a header it is asked to
	// trust; a header is only checked after it is derived from, so a larger
	// value is refused as a modified file.
	maxIterations = 10000000
	saltLen       = 16
	keyLen        = 32
)

// ErrPassphrase is returned when the passphrase is wrong or the file has been
// changed since it was made.
var ErrPassphrase = errors.New("backup passphrase incorrect or file modified: 口令错误或文件被改动")

// Seal encrypts a Bundle with a passphrase and returns the file content.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("口令不能为空")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	h := header(defaultIterations, salt, nonce)
	aad, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(pass, salt, defaultIterations)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, aad)
	h["data"] = base64.StdEncoding.EncodeToString(ct)
	out, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Open decrypts a backup. A file that is not a backup, or is from a newer
// magpie, is reported as such before the passphrase is looked at.
func Open(data []byte, pass string) (Bundle, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Bundle{}, errNotBackup
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return Bundle{}, errNotBackup
	}
	var format string
	if err := json.Unmarshal(m["format"], &format); err != nil || format != Format {
		return Bundle{}, errNotBackup
	}
	var version int
	if err := json.Unmarshal(m["version"], &version); err != nil {
		return Bundle{}, ErrPassphrase
	}
	if version > CurrentVersion {
		return Bundle{}, errNewer
	}
	if version < 1 {
		return Bundle{}, ErrPassphrase
	}
	if pass == "" {
		return Bundle{}, fmt.Errorf("%w（口令为空）", ErrPassphrase)
	}

	// From here on every problem is a header or ciphertext that does not
	// check out: the same answer as a wrong passphrase.
	var kdf string
	if err := json.Unmarshal(m["kdf"], &kdf); err != nil || kdf != KDF {
		return Bundle{}, ErrPassphrase
	}
	var iterations int
	if err := json.Unmarshal(m["iterations"], &iterations); err != nil || iterations < 1 || iterations > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	var saltS, nonceS, dataS string
	if json.Unmarshal(m["salt"], &saltS) != nil || json.Unmarshal(m["nonce"], &nonceS) != nil || json.Unmarshal(m["data"], &dataS) != nil {
		return Bundle{}, ErrPassphrase
	}
	salt, err1 := base64.StdEncoding.DecodeString(saltS)
	nonce, err2 := base64.StdEncoding.DecodeString(nonceS)
	ct, err3 := base64.StdEncoding.DecodeString(dataS)
	if err1 != nil || err2 != nil || err3 != nil || len(salt) == 0 || len(nonce) != 12 {
		return Bundle{}, ErrPassphrase
	}

	// Re-encode the header without the ciphertext: canonical key order, the
	// values as the file spells them. Seal authenticated exactly these bytes.
	delete(m, "data")
	aad, err := json.Marshal(m)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := newGCM(pass, salt, iterations)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	plain, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容已损坏：%w", err)
	}
	return b, nil
}

var (
	errNotBackup = errors.New("不是 magpie 备份文件")
	errNewer     = errors.New("这个备份由更新版本的 magpie 生成，请先升级 magpie 再恢复")
)

// header is the plaintext part of a file, without the ciphertext.
func header(iterations int, salt, nonce []byte) map[string]any {
	return map[string]any{
		"format":     Format,
		"version":    CurrentVersion,
		"kdf":        KDF,
		"iterations": iterations,
		"salt":       base64.StdEncoding.EncodeToString(salt),
		"nonce":      base64.StdEncoding.EncodeToString(nonce),
	}
}

func newGCM(pass string, salt []byte, iterations int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iterations, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
