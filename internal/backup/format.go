// Package backup exports the whole magpie setup to one encrypted file and
// restores it on another machine.
//
// A backup file is a plain JSON header followed by the ciphertext, base64:
//
//	{
//	  "format": "magpie-backup",
//	  "version": 1,
//	  "kdf": "pbkdf2-sha256",
//	  "iterations": 600000,
//	  "salt": "…",
//	  "nonce": "…"
//	}
//	<base64 of AES-256-GCM(JSON of Bundle)>
//
// The key comes from the passphrase by PBKDF2-SHA256. The header bytes are
// the GCM additional data, so changing any header field, iterations included,
// makes the file fail to open with ErrPassphrase.
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
	"strings"
)

// Ext is the extension of a backup file.
const Ext = ".magpie-backup"

// FormatVersion is the newest backup format this magpie reads and writes.
const FormatVersion = 1

const (
	formatName        = "magpie-backup"
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	minIterations     = 100000
	maxIterations     = 10000000
	keyLen            = 32
	saltLen           = 16
	nonceLen          = 12
)

// ErrPassphrase means the passphrase is wrong or the file was changed.
var ErrPassphrase = errors.New("口令错误，或备份文件已被改动")

var errNotBackup = errors.New("不是 magpie 备份文件")

func errNewer(v int) error {
	return fmt.Errorf("这个备份由更新的 magpie 生成（格式版本 %d），请先升级 magpie 再恢复", v)
}

type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts a bundle with the passphrase and returns the file contents.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the passphrase is empty")
	}
	b.Version = FormatVersion
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
	hdr, err := json.MarshalIndent(header{
		Format:     formatName,
		Version:    FormatVersion,
		KDF:        kdfName,
		Iterations: defaultIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(pass, salt, defaultIterations)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plain, hdr)
	out := make([]byte, 0, len(hdr)+1+base64.StdEncoding.EncodedLen(len(ct))+1)
	out = append(out, hdr...)
	out = append(out, '\n')
	out = append(out, base64.StdEncoding.EncodeToString(ct)...)
	out = append(out, '\n')
	return out, nil
}

// Open decrypts a backup file. The checks run in this order: the file must
// be a magpie backup, it must not come from a newer format, and only then is
// the passphrase tried. A bad header that passes the first two checks is
// reported as ErrPassphrase, as a changed file.
func Open(data []byte, pass string) (Bundle, error) {
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return b, errNotBackup
	}
	var hdr header
	if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Format != formatName {
		return b, errNotBackup
	}
	if hdr.Version > FormatVersion {
		return b, errNewer(hdr.Version)
	}
	if hdr.Version < 1 || hdr.KDF != kdfName || hdr.Iterations < minIterations || hdr.Iterations > maxIterations {
		return b, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(hdr.Salt)
	if err != nil || len(salt) != saltLen {
		return b, ErrPassphrase
	}
	nonce, err := base64.StdEncoding.DecodeString(hdr.Nonce)
	if err != nil || len(nonce) != nonceLen {
		return b, ErrPassphrase
	}
	body := strings.Join(strings.Fields(string(data[int(dec.InputOffset()):])), "")
	ct, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return b, ErrPassphrase
	}
	aead, err := newAEAD(pass, salt, hdr.Iterations)
	if err != nil {
		return b, err
	}
	plain, err := aead.Open(nil, nonce, ct, []byte(raw))
	if err != nil {
		return b, ErrPassphrase
	}
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容无法读取: %w", err)
	}
	if b.Version > FormatVersion {
		return Bundle{}, errNewer(b.Version)
	}
	return b, nil
}

func newAEAD(pass string, salt []byte, iterations int) (cipher.AEAD, error) {
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
