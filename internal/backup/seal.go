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
)

// A backup file is one line of plain JSON — the header — then a line of
// base64 ciphertext. The header holds only what is needed to open the file
// (format, version, key derivation, salt, nonce). It is the AES-GCM
// additional data, byte for byte, so changing any part of it, the iteration
// count included, makes the file fail to open.

const (
	format    = "magpie-backup"
	kdfName   = "pbkdf2-sha256"
	keyLen    = 32
	saltLen   = 16
	nonceLen  = 12
	maxSalt   = 256
	maxIters  = 10000000 // refuses a header asking for absurd work
	iterCount = 600000   // what Seal writes
)

type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts b under pass and returns the file's contents.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the password is empty")
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	head, err := json.Marshal(header{
		Format:     format,
		Version:    FormatVersion,
		KDF:        kdfName,
		Iterations: iterCount,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	})
	if err != nil {
		return nil, err
	}
	// the header is written as `"iterations": N`, with one space after the colon
	head = []byte(strings.Replace(string(head), `"iterations":`, `"iterations": `, 1))
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(pass, salt, iterCount)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plain, head)

	var out bytes.Buffer
	out.Write(head)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(ct))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. The checks run in order: the file must be
// a magpie backup (ErrNotBackup), must not come from a newer magpie
// (ErrNewer), and must open with pass (ErrPassphrase).
func Open(data []byte, pass string) (Bundle, error) {
	var b Bundle
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return b, ErrNotBackup
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return b, ErrNotBackup
	}
	var probe struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Format != format {
		return b, ErrNotBackup
	}
	if probe.Version > FormatVersion {
		return b, ErrNewer
	}
	if probe.Version < 1 {
		return b, ErrNotBackup
	}

	// From here on every problem means the password is wrong or the file
	// was changed: the two cannot be told apart, and say so.
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return b, ErrPassphrase
	}
	if pass == "" {
		return b, fmt.Errorf("%w: no password given", ErrPassphrase)
	}
	if h.KDF != kdfName || h.Iterations < 1 || h.Iterations > maxIters {
		return b, ErrPassphrase
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) == 0 || len(salt) > maxSalt {
		return b, ErrPassphrase
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(nonce) != nonceLen {
		return b, ErrPassphrase
	}
	body := strings.Join(strings.Fields(string(data[dec.InputOffset():])), "")
	ct, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return b, ErrPassphrase
	}
	aead, err := newAEAD(pass, salt, h.Iterations)
	if err != nil {
		return b, ErrPassphrase
	}
	plain, err := aead.Open(nil, nonce, ct, raw)
	if err != nil {
		return b, ErrPassphrase
	}
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("the backup's contents are damaged: %w", err)
	}
	return b, nil
}

func newAEAD(pass string, salt []byte, iters int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iters, keyLen)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}
