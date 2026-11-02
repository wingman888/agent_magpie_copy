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
	"strconv"
)

// A backup file is one line of plain JSON, the header, then the sealed
// bundle in base64:
//
//	{"format": "magpie-backup", "version": 1, "kdf": "pbkdf2-sha256", "iterations": 600000, "salt": "…", "nonce": "…"}
//	<base64 AES-256-GCM ciphertext>
//
// The key is PBKDF2-SHA256 of the passphrase over the salt, and the header
// line, byte for byte, is the cipher's additional data: a header changed in
// any way — fewer iterations, another salt — fails to open just as a wrong
// passphrase does.

// Ext is the extension backup files carry.
const Ext = ".magpie-backup"

const (
	format = "magpie-backup"
	// Version is the newest file format this magpie reads and the one it
	// writes.
	Version = 1
	kdfName = "pbkdf2-sha256"
	// iterations is what Seal derives the key with.
	iterations = 600000
	// minIterations and maxIterations bound what Open will try: below is
	// too weak to have been written by magpie, above would hang on a
	// tampered header.
	minIterations = 100000
	maxIterations = 10000000
	saltLen       = 16
	keyLen        = 32
)

var (
	// ErrPassphrase is a passphrase that doesn't open the file, or a file
	// that was changed after it was written: the two can't be told apart.
	ErrPassphrase = errors.New("wrong passphrase or the file has been modified (口令错误或文件被改动)")
	// ErrNotBackup is a file that isn't a magpie backup at all.
	ErrNotBackup = errors.New("not a magpie backup file (不是 magpie 备份)")
	// ErrTooNew is a backup a newer magpie wrote, in a format this one
	// doesn't know.
	ErrTooNew = errors.New("this backup was made by a newer magpie; upgrade magpie to restore it (需升级 magpie)")
	// ErrNoPassphrase is an empty passphrase given to Seal.
	ErrNoPassphrase = errors.New("the passphrase is empty")
)

// Seal encrypts a bundle with a passphrase and returns the file's content.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, ErrNoPassphrase
	}
	if b.Version == 0 {
		b.Version = Version
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := newAEAD(pass, salt, iterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding
	header := fmt.Sprintf(`{"format": %q, "version": %d, "kdf": %q, "iterations": %d, "salt": %q, "nonce": %q}`,
		format, Version, kdfName, iterations, enc.EncodeToString(salt), enc.EncodeToString(nonce))
	sealed := aead.Seal(nil, nonce, plain, []byte(header))
	var out bytes.Buffer
	out.WriteString(header)
	out.WriteByte('\n')
	out.WriteString(enc.EncodeToString(sealed))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. A file that isn't a backup is ErrNotBackup,
// one from a newer magpie ErrTooNew, and only then is a wrong passphrase or
// a changed file ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	header, body, _ := bytes.Cut(data, []byte("\n"))
	header = bytes.TrimSuffix(header, []byte("\r"))
	var h map[string]json.RawMessage
	if len(bytes.TrimSpace(header)) == 0 || json.Unmarshal(header, &h) != nil {
		return Bundle{}, ErrNotBackup
	}
	var f string
	if json.Unmarshal(h["format"], &f) != nil || f != format {
		return Bundle{}, ErrNotBackup
	}
	v, err := strconv.Atoi(string(bytes.TrimSpace(h["version"])))
	if err != nil || v < 1 {
		return Bundle{}, ErrNotBackup
	}
	if v > Version {
		return Bundle{}, ErrTooNew
	}

	var kdf, salt64, nonce64 string
	if json.Unmarshal(h["kdf"], &kdf) != nil || kdf != kdfName {
		return Bundle{}, ErrPassphrase
	}
	iter, err := strconv.Atoi(string(bytes.TrimSpace(h["iterations"])))
	if err != nil || iter < minIterations || iter > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	if json.Unmarshal(h["salt"], &salt64) != nil || json.Unmarshal(h["nonce"], &nonce64) != nil {
		return Bundle{}, ErrPassphrase
	}
	enc := base64.StdEncoding
	salt, err1 := enc.DecodeString(salt64)
	nonce, err2 := enc.DecodeString(nonce64)
	sealed, err3 := enc.DecodeString(string(bytes.TrimSpace(body)))
	if err1 != nil || err2 != nil || err3 != nil || len(salt) < 8 {
		return Bundle{}, ErrPassphrase
	}
	if pass == "" {
		return Bundle{}, ErrPassphrase
	}
	aead, err := newAEAD(pass, salt, iter)
	if err != nil {
		return Bundle{}, err
	}
	if len(nonce) != aead.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := aead.Open(nil, nonce, sealed, header)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("the backup opened but can't be read: %v", err)
	}
	if b.Version > Version {
		return Bundle{}, ErrTooNew
	}
	return b, nil
}

func newAEAD(pass string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
