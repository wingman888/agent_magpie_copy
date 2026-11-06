// Package backup packs magpie's configuration into one encrypted file and
// unpacks it again.
//
// File layout: one line of plain JSON (the header), a newline, then the
// base64 of the AES-256-GCM ciphertext. The header bytes are the GCM additional
// authenticated data, so changing any byte of the header, the iteration count
// included, makes decryption fail. The key is derived from the secret with
// PBKDF2-SHA256.
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

	"github.com/yetone/magpie/internal/provider"
)

// Ext is the extension of a backup file.
const Ext = ".magpie-backup"

const (
	formatName = "magpie-backup"
	// version is the newest file version this build reads and writes.
	version = 1
	kdfName = "pbkdf2-sha256"
	// defaultIterations is what Seal writes. Open refuses anything below
	// minIterations, and anything above maxIterations as a guard against
	// a header that would make Open spin for minutes.
	defaultIterations = 600000
	minIterations     = 100000
	maxIterations     = 10000000
	saltLen           = 16
	nonceLen          = 12
	keyLen            = 32
)

// ErrPassphrase means the secret is wrong or the file was modified after it
// was written. Check it with errors.Is.
var ErrPassphrase = errors.New("wrong secret, or the backup file was modified")

// ErrNotBackup means the data is not a magpie backup at all.
var ErrNotBackup = errors.New("不是 magpie 备份文件")

// ErrNewer means the backup was written by a newer magpie than this one.
var ErrNewer = errors.New("备份由更新版本的 magpie 生成，请先升级 magpie")

// Bundle is everything a backup carries. Version is 1 for now.
type Bundle struct {
	Version   int                 `json:"version"`
	App       string              `json:"app,omitempty"` // the magpie version that wrote it
	Providers []provider.Provider `json:"providers"`
	// Icons maps a file name in the icons directory to its content.
	Icons map[string][]byte `json:"icons,omitempty"`
	// Settings is the settings document; empty means the backup has none.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Profiles is the saved profiles document; empty means none.
	Profiles json.RawMessage `json:"profiles,omitempty"`
	// Agents is the per-agent model settings; empty means none.
	Agents json.RawMessage `json:"agents,omitempty"`
}

// header is the plain JSON line at the top of a backup file.
type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

func deriveKey(pass string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iterations, keyLen)
}

// Seal encrypts the bundle with the secret and returns the file contents.
// The secret must not be empty.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the backup secret must not be empty")
	}
	if b.Version == 0 {
		b.Version = version
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encode backup: %w", err)
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("random salt: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("random nonce: %w", err)
	}
	// The values are base64 and fixed ASCII names, so they need no escaping.
	head := fmt.Sprintf(`{"format": "%s", "version": %d, "kdf": "%s", "iterations": %d, "salt": "%s", "nonce": "%s"}`,
		formatName, version, kdfName, defaultIterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(nonce))

	key, err := deriveKey(pass, salt, defaultIterations)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, []byte(head))

	var out bytes.Buffer
	out.WriteString(head)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(ct))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. It checks the format first, then the version,
// and only then the secret: a wrong secret or a changed file yields ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	var none Bundle
	data = bytes.TrimLeft(data, " \t\r\n")
	if len(data) == 0 {
		return none, ErrNotBackup
	}
	head, rest, _ := bytes.Cut(data, []byte("\n"))

	var probe struct {
		Format  string          `json:"format"`
		Version json.RawMessage `json:"version"`
	}
	if json.Unmarshal(head, &probe) != nil {
		// The first line is not a whole JSON document. If the data still
		// looks like a magpie header, the header was split or altered.
		if looksLikeHeader(data) {
			return none, ErrPassphrase
		}
		return none, ErrNotBackup
	}
	if probe.Format != formatName {
		return none, ErrNotBackup
	}
	var ver int
	if json.Unmarshal(probe.Version, &ver) != nil || ver < 1 {
		// The format is right but the version is not a usable number:
		// the header was altered.
		return none, ErrPassphrase
	}
	if ver > version {
		return none, ErrNewer
	}

	var h header
	if json.Unmarshal(head, &h) != nil {
		return none, ErrPassphrase
	}
	if h.KDF != kdfName || h.Iterations < minIterations || h.Iterations > maxIterations {
		return none, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) != saltLen {
		return none, ErrPassphrase
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(nonce) != nonceLen {
		return none, ErrPassphrase
	}
	ct, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(rest)))
	if err != nil {
		return none, ErrPassphrase
	}
	if pass == "" {
		return none, ErrPassphrase
	}

	key, err := deriveKey(pass, salt, h.Iterations)
	if err != nil {
		return none, ErrPassphrase
	}
	gcm, err := newGCM(key)
	if err != nil {
		return none, err
	}
	plain, err := gcm.Open(nil, nonce, ct, head)
	if err != nil {
		return none, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return none, fmt.Errorf("backup contents are damaged: %w", err)
	}
	b.Version = ver
	return b, nil
}

// looksLikeHeader reports whether data begins like a magpie header, even if
// that header no longer parses as JSON.
func looksLikeHeader(data []byte) bool {
	return len(data) > 0 && data[0] == '{' &&
		bytes.Contains(data, []byte(`"format"`)) &&
		bytes.Contains(data, []byte(formatName))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	return gcm, nil
}
