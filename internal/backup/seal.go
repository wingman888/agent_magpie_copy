package backup

// The file format: one line of plain JSON (the header), a newline, then the
// ciphertext as base64. The header is the AES-GCM additional data, so any
// change to it, iterations included, fails authentication.

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

// Ext is the file extension of a backup.
const Ext = ".magpie-backup"

const (
	formatName   = "magpie-backup"
	currentVer   = 1
	kdfName      = "pbkdf2-sha256"
	defaultIters = 600000
	minIters     = 100000
	maxIters     = 10000000
	saltLen      = 16
	nonceLen     = 12
	keyLen       = 32
)

// ErrPassphrase means the passphrase is wrong, or the file was changed.
var ErrPassphrase = errors.New("口令错误或文件被改动")

var errNotBackup = errors.New("不是 magpie 备份")

type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts b under pass and returns the file contents.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("口令不能为空")
	}
	b.Version = currentVer
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	h := header{
		Format:     formatName,
		Version:    currentVer,
		KDF:        kdfName,
		Iterations: defaultIters,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	}
	hb, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(pass, salt, h.Iterations)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plain, hb)

	var out bytes.Buffer
	out.Write(hb)
	out.WriteByte('\n')
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(ct)))
	base64.StdEncoding.Encode(enc, ct)
	out.Write(enc)
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup. The checks run in order: the file must be a magpie
// backup, its version must be one this build reads, and only then is the
// passphrase tried, failing with ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	var b Bundle
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return b, errNotBackup
	}
	start := len(data) - len(trimmed)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var h header
	if err := dec.Decode(&h); err != nil || h.Format != formatName {
		return b, errNotBackup
	}
	if h.Version > currentVer {
		return b, fmt.Errorf("需升级 magpie 到更新版本：备份格式版本为 %d，本机最高支持 %d", h.Version, currentVer)
	}
	if h.Version < 1 {
		return b, errNotBackup
	}
	end := start + int(dec.InputOffset())
	hb := data[start:end]

	// From here on, anything wrong with the header or the body is reported
	// as a passphrase/tamper failure.
	if h.KDF != kdfName || h.Iterations < minIters || h.Iterations > maxIters {
		return b, ErrPassphrase
	}
	salt, err1 := base64.StdEncoding.DecodeString(h.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(h.Nonce)
	if err1 != nil || err2 != nil || len(salt) != saltLen || len(nonce) != nonceLen {
		return b, ErrPassphrase
	}
	ct, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data[end:])))
	if err != nil {
		return b, ErrPassphrase
	}
	key, err := deriveKey(pass, salt, h.Iterations)
	if err != nil {
		return b, ErrPassphrase
	}
	aead, err := newGCM(key)
	if err != nil {
		return b, err
	}
	plain, err := aead.Open(nil, nonce, ct, hb)
	if err != nil {
		return b, ErrPassphrase
	}
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容无法读取: %w", err)
	}
	return b, nil
}

func deriveKey(pass string, salt []byte, iters int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iters, keyLen)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
