package backup

// The file is a plain JSON header, a newline, then the base64 of the sealed
// bundle. The header carries the format, version, key-derivation settings,
// a salt and a nonce. Its exact bytes are the AES-GCM additional data, so
// changing any header field, iterations included, makes the file fail to
// open the same way a wrong passphrase does.

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

const (
	formatName        = "magpie-backup"
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	maxIterations     = 10000000
	saltLen           = 16
	keyLen            = 32
)

// ErrPassphrase means the passphrase is wrong or the file was altered after
// it was written. Test for it with errors.Is.
var ErrPassphrase = errors.New("wrong password, or the backup file was changed")

// errNotBackup means the data is not a magpie backup at all.
var errNotBackup = errors.New("不是 magpie 备份文件")

func errNewer(v int) error {
	return fmt.Errorf("这个备份由更新版本的 magpie 生成（备份版本 %d，本机最高支持 %d），请先升级 magpie", v, version)
}

// header is the plain-text part of a backup file.
type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
}

// Seal encrypts b under pass and returns the file's content.
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
	gcm, err := deriveGCM(pass, salt, defaultIterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	hdr, err := json.MarshalIndent(header{
		Format:     formatName,
		Version:    version,
		KDF:        kdfName,
		Iterations: defaultIterations,
		Salt:       salt,
		Nonce:      nonce,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, payload, hdr)

	var out bytes.Buffer
	out.Write(hdr)
	out.WriteByte('\n')
	out.WriteString(base64.StdEncoding.EncodeToString(ct))
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Open decrypts a backup file. It tells apart data that is not a backup,
// a backup from a newer magpie, and a wrong passphrase or altered file.
func Open(data []byte, pass string) (Bundle, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return Bundle{}, errNotBackup
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Bundle{}, errNotBackup
	}
	var format string
	if err := json.Unmarshal(fields["format"], &format); err != nil || format != formatName {
		return Bundle{}, errNotBackup
	}
	var v int
	if err := json.Unmarshal(fields["version"], &v); err != nil || v < 1 {
		return Bundle{}, errNotBackup
	}
	if v > version {
		return Bundle{}, errNewer(v)
	}
	if pass == "" {
		return Bundle{}, errors.New("口令不能为空")
	}

	// From here on, anything wrong with the file is reported as ErrPassphrase.
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		return Bundle{}, ErrPassphrase
	}
	if h.KDF != kdfName || h.Iterations < 1 || h.Iterations > maxIterations || len(h.Salt) == 0 {
		return Bundle{}, ErrPassphrase
	}
	body := bytes.Join(bytes.Fields(data[dec.InputOffset():]), nil)
	ct, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := deriveGCM(pass, h.Salt, h.Iterations)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	if len(h.Nonce) != gcm.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := gcm.Open(nil, h.Nonce, ct, raw)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容已损坏: %w", err)
	}
	return b, nil
}

func deriveGCM(pass string, salt []byte, iterations int) (cipher.AEAD, error) {
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
