package backup

// The file is one JSON object: the header fields in clear, then "data", the
// base64 ciphertext of the bundle's JSON. Every header field is bound to the
// ciphertext as AES-GCM additional data, so changing any of them (the
// iteration count included) makes Open fail with ErrPassphrase.

import (
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

// ErrPassphrase means the passphrase is wrong or the file was changed.
var ErrPassphrase = errors.New("口令错误或文件已被改动")

var (
	errNotBackup = errors.New("不是 magpie 备份文件")
	errNewer     = errors.New("这个备份由更新版本的 magpie 生成，需升级 magpie 后再恢复")
)

const (
	formatName        = "magpie-backup"
	formatVersion     = 1
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	minIterations     = 100000
	maxIterations     = 10000000
	saltLen           = 16
	nonceLen          = 12
	keyLen            = 32
)

// headerFields is the clear part of the file, everything but "data".
func headerFields(iterations int, salt, nonce []byte) map[string]any {
	return map[string]any{
		"format":     formatName,
		"version":    formatVersion,
		"kdf":        kdfName,
		"iterations": iterations,
		"salt":       base64.StdEncoding.EncodeToString(salt),
		"nonce":      base64.StdEncoding.EncodeToString(nonce),
	}
}

func deriveKey(pass string, salt []byte, iterations int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iterations, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts the bundle with the passphrase and returns the file contents.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("口令不能为空")
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
	key, err := deriveKey(pass, salt, defaultIterations)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	header := headerFields(defaultIterations, salt, nonce)
	aad, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, aad)
	out := headerFields(defaultIterations, salt, nonce)
	out["data"] = base64.StdEncoding.EncodeToString(ct)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Open decrypts a backup file. The checks run in this order: the file must be
// a backup at all, its version must be one this build understands, and only
// then does the passphrase count.
func Open(data []byte, pass string) (Bundle, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Bundle{}, errNotBackup
	}
	var format string
	if json.Unmarshal(raw["format"], &format) != nil || format != formatName {
		return Bundle{}, errNotBackup
	}
	var ver int
	if json.Unmarshal(raw["version"], &ver) != nil || ver < 1 {
		return Bundle{}, ErrPassphrase
	}
	if ver > formatVersion {
		return Bundle{}, errNewer
	}
	var kdf string
	if json.Unmarshal(raw["kdf"], &kdf) != nil || kdf != kdfName {
		return Bundle{}, ErrPassphrase
	}
	var iterations int
	if json.Unmarshal(raw["iterations"], &iterations) != nil ||
		iterations < minIterations || iterations > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	var saltS, nonceS, dataS string
	if json.Unmarshal(raw["salt"], &saltS) != nil || json.Unmarshal(raw["nonce"], &nonceS) != nil ||
		json.Unmarshal(raw["data"], &dataS) != nil {
		return Bundle{}, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(saltS)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceS)
	if err != nil || len(nonce) != nonceLen {
		return Bundle{}, ErrPassphrase
	}
	ct, err := base64.StdEncoding.DecodeString(dataS)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	// AAD is every header field, re-marshalled canonically: map keys are
	// sorted and raw values are compacted, so whitespace edits don't matter
	// but any value change does.
	delete(raw, "data")
	aad, err := json.Marshal(raw)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	key, err := deriveKey(pass, salt, iterations)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Bundle{}, err
	}
	plain, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w", ErrPassphrase)
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容已损坏: %w", err)
	}
	if b.Version > formatVersion {
		return Bundle{}, errNewer
	}
	return b, nil
}
