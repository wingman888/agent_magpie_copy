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
	"io"
)

// A backup file is one line of plain JSON, the header, then a newline and the
// base64 of the ciphertext:
//
//	{"format": "magpie-backup", "version": 1, "kdf": "pbkdf2-sha256", "iterations": 600000, "salt": "…", "nonce": "…"}
//	<base64 of AES-256-GCM ciphertext>
//
// The key is PBKDF2-SHA256 of the passphrase. The header bytes, exactly as
// written, are the GCM additional data, so changing any header field — the
// iteration count included — makes the file fail to open.

const (
	formatName   = "magpie-backup"
	kdfName      = "pbkdf2-sha256"
	defaultIters = 600000
	minIters     = 100000 // below this a header is treated as tampered with
	maxIters     = 10000000
	saltLen      = 16
	keyLen       = 32
)

var (
	// ErrPassphrase means the passphrase is wrong or the file was changed.
	ErrPassphrase = errors.New("口令错误或文件被改动")

	// ErrNotBackup means the data is not a magpie backup at all.
	ErrNotBackup = errors.New("不是 magpie 备份文件")

	// ErrNewer means the backup was made by a newer magpie than this one.
	ErrNewer = errors.New("这份备份由更新版本的 magpie 生成，请先升级 magpie 再恢复")
)

// header is the plain part of a backup file.
type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts b with pass and returns the bytes of a backup file.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("口令不能为空")
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	gcm, err := newGCM(pass, salt, defaultIters)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	hdr := fmt.Sprintf(`{"format": %q, "version": %d, "kdf": %q, "iterations": %d, "salt": %q, "nonce": %q}`,
		formatName, FormatVersion, kdfName, defaultIters,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(nonce))
	sealed := gcm.Seal(nil, nonce, payload, []byte(hdr))

	var out bytes.Buffer
	out.WriteString(hdr)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(sealed))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file with pass. A file that is not a backup, or
// that a newer magpie wrote, is reported as such before the passphrase is
// looked at; a bad passphrase or a changed file gives ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Bundle{}, ErrNotBackup
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&fields); err != nil {
		return Bundle{}, ErrNotBackup
	}
	end := int(dec.InputOffset())

	var format string
	if json.Unmarshal(fields["format"], &format) != nil || format != formatName {
		return Bundle{}, ErrNotBackup
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || version < 1 {
		return Bundle{}, ErrPassphrase
	}
	if version > FormatVersion {
		return Bundle{}, fmt.Errorf("%w（备份版本 %d，本机支持到 %d）", ErrNewer, version, FormatVersion)
	}

	var h header
	if json.Unmarshal(trimmed[:end], &h) != nil || h.KDF != kdfName ||
		h.Iterations < minIters || h.Iterations > maxIters {
		return Bundle{}, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) < saltLen {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := newGCM(pass, salt, h.Iterations)
	if err != nil {
		return Bundle{}, err
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	sealed, err := base64.StdEncoding.DecodeString(string(trimmed[end:]))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	payload, err := gcm.Open(nil, nonce, sealed, trimmed[:end])
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(payload, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容损坏：%w", err)
	}
	if b.Version > FormatVersion {
		return Bundle{}, ErrNewer
	}
	return b, nil
}

// newGCM derives the key for pass and returns an AES-256-GCM cipher on it.
// An empty passphrase is refused.
func newGCM(pass string, salt []byte, iters int) (cipher.AEAD, error) {
	if pass == "" {
		return nil, errors.New("口令不能为空")
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, iters, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
