// Package backup exports magpie's configuration to a single file sealed with a
// passphrase, and reads such a file back for restoring on another machine.
//
// File layout: one line of plain JSON header, a newline, then the base64 of the
// AES-256-GCM ciphertext. The whole header is the GCM additional data, so any
// change to it (including the iteration count) fails authentication.
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
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Ext is the file extension of a backup file.
const Ext = ".magpie-backup"

const (
	formatName        = "magpie-backup"
	formatVersion     = 1
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	maxIterations     = 10000000
	saltSize          = 16
	keySize           = 32
)

// ErrPassphrase means the passphrase is wrong or the file was changed.
var ErrPassphrase = errors.New("密码错误或备份文件已被改动")

var (
	errNotBackup  = errors.New("不是 magpie 备份文件")
	errNewer      = errors.New("备份由更新版本的 magpie 生成，请升级 magpie")
	errEmptyPass  = errors.New("密码不能为空")
	errCorruptDoc = errors.New("备份内容已损坏")
)

// Bundle is everything a backup carries. Collect fills it from this machine;
// Restore writes the selected parts back.
type Bundle struct {
	Version   int                 `json:"version"`
	Providers []provider.Provider `json:"providers"`
}

// header is the plain-text part of the file. Its fields are authenticated as
// additional data, so it is only ever read after the format checks pass.
type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts b with the passphrase and returns the file contents.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errEmptyPass
	}
	b.Version = formatVersion
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("编码备份失败: %w", err)
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := deriveKey(pass, salt, defaultIterations)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	hb, err := json.Marshal(header{
		Format:     formatName,
		Version:    formatVersion,
		KDF:        kdfName,
		Iterations: defaultIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	})
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plain, hb)

	var out bytes.Buffer
	out.Write(hb)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(ct))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. The format is checked before the passphrase:
// non-backups and newer versions get their own errors, never ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	raw, rest, err := splitHeader(data)
	if err != nil {
		return Bundle{}, err
	}

	var probe struct {
		Format  string          `json:"format"`
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Format != formatName {
		return Bundle{}, errNotBackup
	}
	var v int
	if err := json.Unmarshal(probe.Version, &v); err != nil || v < 1 {
		return Bundle{}, ErrPassphrase
	}
	if v > formatVersion {
		return Bundle{}, errNewer
	}

	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return Bundle{}, ErrPassphrase
	}
	if h.KDF != kdfName || h.Iterations < 1 || h.Iterations > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) == 0 {
		return Bundle{}, ErrPassphrase
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(rest)))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}

	if pass == "" {
		return Bundle{}, errEmptyPass
	}
	key, err := deriveKey(pass, salt, h.Iterations)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	aead, err := newAEAD(key)
	if err != nil {
		return Bundle{}, err
	}
	if len(nonce) != aead.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := aead.Open(nil, nonce, ct, raw)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}

	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", errCorruptDoc, err)
	}
	return b, nil
}

// splitHeader returns the exact bytes of the leading JSON value (the header,
// which is the authenticated data) and whatever follows it.
func splitHeader(data []byte) (raw, rest []byte, err error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, errNotBackup
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		// A damaged header of a real backup is a changed file, not a foreign one.
		if bytes.Contains(data, []byte(`"`+formatName+`"`)) {
			return nil, nil, ErrPassphrase
		}
		return nil, nil, errNotBackup
	}
	return v, data[int(dec.InputOffset()):], nil
}

func deriveKey(pass string, salt []byte, iterations int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iterations, keySize)
	if err != nil {
		return nil, fmt.Errorf("派生密钥失败: %w", err)
	}
	return key, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
