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

// A backup file is two lines of text: a plain JSON header, then the sealed
// bundle in base64. The whole header is the AES-GCM additional data, so
// changing any byte of it, iterations included, fails authentication.
//
//	{"format": "magpie-backup", "version": 1, "kdf": "pbkdf2-sha256", "iterations": 600000, "salt": "…", "nonce": "…"}
//	<base64 of GCM(ciphertext + tag)>

// Ext is the extension a backup file is given.
const Ext = ".magpie-backup"

const (
	formatName        = "magpie-backup"
	formatVersion     = 1
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	// maxIterations keeps a tampered header from asking for hours of work
	// before the authentication check that would reject it anyway.
	maxIterations = 10000000
	saltLen       = 16
	keyLen        = 32
)

// ErrPassphrase means the passphrase is wrong or the file was changed.
var ErrPassphrase = errors.New("口令错误或文件被改动")

var (
	errNotBackup = errors.New("不是 magpie 备份文件")
	errNewer     = errors.New("这个备份由更新的 magpie 生成，请先升级 magpie 再恢复")
)

// Seal encrypts a bundle under pass and returns the file content.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("口令为空")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
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
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	hdr := header(salt, nonce, defaultIterations)
	ct := gcm.Seal(nil, nonce, plain, hdr)

	var out bytes.Buffer
	out.Write(hdr)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(ct))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. The checks run in order: the file must be a
// magpie backup at all, then it must not be from a newer magpie, and only then
// is the passphrase tried, which fails with ErrPassphrase for a wrong
// passphrase or any change to the file.
func Open(data []byte, pass string) (Bundle, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return Bundle{}, errNotBackup
	}
	end := int(dec.InputOffset())
	hdr := trimmed[:end]
	body := trimmed[end:]

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(hdr, &fields); err != nil {
		return Bundle{}, errNotBackup
	}
	var format string
	if err := json.Unmarshal(fields["format"], &format); err != nil || format != formatName {
		return Bundle{}, errNotBackup
	}
	var version int
	if err := json.Unmarshal(fields["version"], &version); err != nil || version < 1 {
		return Bundle{}, errNotBackup
	}
	if version > formatVersion {
		return Bundle{}, errNewer
	}

	var h struct {
		KDF        string `json:"kdf"`
		Iterations int    `json:"iterations"`
		Salt       []byte `json:"salt"`
		Nonce      []byte `json:"nonce"`
	}
	if err := json.Unmarshal(hdr, &h); err != nil {
		return Bundle{}, ErrPassphrase
	}
	if h.KDF != kdfName || h.Iterations < 1 || h.Iterations > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	ct, err := base64.StdEncoding.DecodeString(string(bytes.Join(bytes.Fields(body), nil)))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	key, err := deriveKey(pass, h.Salt, h.Iterations)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Bundle{}, err
	}
	if len(h.Nonce) != gcm.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := gcm.Open(nil, h.Nonce, ct, hdr)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, errors.New("备份内容已损坏")
	}
	return b, nil
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

// header is the plain JSON that opens a backup file. Its fields are ASCII
// and the spacing is fixed, so the bytes written are the bytes authenticated.
func header(salt, nonce []byte, iterations int) []byte {
	q := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return []byte(fmt.Sprintf(`{"format": %s, "version": %d, "kdf": %s, "iterations": %d, "salt": %s, "nonce": %s}`,
		q(formatName), formatVersion, q(kdfName), iterations,
		q(base64.StdEncoding.EncodeToString(salt)), q(base64.StdEncoding.EncodeToString(nonce))))
}
