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

// File layout: a plain-text JSON header, then the sealed Bundle as base64.
//
//	{
//	  "format": "magpie-backup",
//	  "version": 1,
//	  "kdf": "pbkdf2-sha256",
//	  "iterations": 600000,
//	  "salt": "...",
//	  "nonce": "..."
//	}
//	<base64 of AES-256-GCM ciphertext>
//
// The header bytes are the GCM additional data, so changing any header field
// (the iteration count included) makes the file fail to open.

const (
	formatName    = "magpie-backup"
	formatVersion = 1
	kdfName       = "pbkdf2-sha256"
	defaultIters  = 600000
	maxIters      = 10000000 // above this a header is hostile or broken, not a real backup
	saltSize      = 16
	keySize       = 32
	nonceSize     = 12
)

// ErrPassphrase means the backup could not be opened: the secret is wrong,
// or the file was changed after it was made.
var ErrPassphrase = errors.New("decryption failed: the password is wrong or the file was altered")

// header is the plain-text part of a backup file.
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
		return nil, errors.New("the backup needs a non-empty password")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltSize)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	h := header{
		Format:     formatName,
		Version:    formatVersion,
		KDF:        kdfName,
		Iterations: defaultIters,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	}
	hdr, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(pass, salt, defaultIters)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, hdr)

	var out bytes.Buffer
	out.Write(hdr)
	out.WriteByte('\n')
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(sealed)))
	base64.StdEncoding.Encode(enc, sealed)
	out.Write(enc)
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file made by Seal.
func Open(data []byte, pass string) (Bundle, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Bundle{}, ErrNotBackup
	}
	// Read the header as the first JSON value; whatever follows is the body.
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return Bundle{}, ErrNotBackup
	}
	hdrEnd := int(dec.InputOffset())
	hdrBytes := data[:hdrEnd]

	var probe struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Format != formatName {
		return Bundle{}, ErrNotBackup
	}
	if probe.Version > formatVersion {
		return Bundle{}, errNewer
	}
	if probe.Version < 1 {
		return Bundle{}, ErrNotBackup
	}

	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return Bundle{}, tampered()
	}
	if h.KDF != kdfName || h.Iterations < 1 || h.Iterations > maxIters {
		return Bundle{}, tampered()
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) == 0 {
		return Bundle{}, tampered()
	}
	nonce, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return Bundle{}, tampered()
	}
	body := bytes.TrimSpace(data[hdrEnd:])
	sealed := make([]byte, base64.StdEncoding.DecodedLen(len(body)))
	n, err := base64.StdEncoding.Decode(sealed, body)
	if err != nil {
		return Bundle{}, tampered()
	}
	sealed = sealed[:n]

	if pass == "" {
		return Bundle{}, ErrPassphrase
	}
	key, err := deriveKey(pass, salt, h.Iterations)
	if err != nil {
		return Bundle{}, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Bundle{}, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, hdrBytes)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("the backup content is damaged: %w", err)
	}
	return b, nil
}

// tampered is ErrPassphrase for a header that is no longer what Seal wrote.
func tampered() error { return ErrPassphrase }

func deriveKey(pass string, salt []byte, iters int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iters, keySize)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}
