package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// File format. A backup file is one plain JSON header whose "data" field
// holds the AES-256-GCM ciphertext of the Bundle's JSON. Everything else in
// the header is the GCM additional data, so changing any header field makes
// the file fail to open.
const (
	formatName        = "magpie-backup"
	formatVersion     = 1
	kdfName           = "pbkdf2-sha256"
	defaultIterations = 600000
	minIterations     = 100000 // a file must say more than this
	maxIterations     = 10000000
	saltLen           = 16
	keyLen            = 32
)

// header is the plain JSON head of a backup file. Salt, nonce and data are
// base64 (standard encoding).
type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Data       string `json:"data"`
}

// headerAAD is the header without the ciphertext: the bytes GCM authenticates.
// Sealing and opening both build it from the same fields, in this order, so
// the encoding is reproducible.
type headerAAD struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// aad returns the additional data for the header's fields. Marshalling a
// struct of strings and ints cannot fail.
func (h header) aad() []byte {
	b, _ := json.Marshal(headerAAD{
		Format:     h.Format,
		Version:    h.Version,
		KDF:        h.KDF,
		Iterations: h.Iterations,
		Salt:       h.Salt,
		Nonce:      h.Nonce,
	})
	return b
}

// Seal encrypts b with pass and returns the backup file's content. pass must
// not be empty.
func Seal(b Bundle, pass string) ([]byte, error) {
	return seal(b, pass, defaultIterations)
}

func seal(b Bundle, pass string, iterations int) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("备份密码不能为空")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("编码备份内容: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("生成随机盐: %w", err)
	}
	key := pbkdf2SHA256([]byte(pass), salt, iterations, keyLen)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成随机数: %w", err)
	}

	h := header{
		Format:     formatName,
		Version:    formatVersion,
		KDF:        kdfName,
		Iterations: iterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
	}
	ct := gcm.Seal(nil, nonce, plain, h.aad())
	h.Data = base64.StdEncoding.EncodeToString(ct)

	out, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码备份文件: %w", err)
	}
	return append(out, '\n'), nil
}

// Open decrypts a backup file with pass. The format is checked before the
// passphrase: data that is not a backup gives ErrNotBackup, a backup from a
// newer magpie gives ErrNewerVersion, and only a valid header that fails to
// decrypt gives ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	var probe struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		// A right format with a mangled version is a tampered header.
		var typeErr *json.UnmarshalTypeError
		if probe.Format == formatName && errors.As(err, &typeErr) {
			return Bundle{}, fmt.Errorf("%w: 头部版本号无效", ErrPassphrase)
		}
		return Bundle{}, ErrNotBackup
	}
	if probe.Format != formatName {
		return Bundle{}, ErrNotBackup
	}
	if probe.Version > formatVersion {
		return Bundle{}, ErrNewerVersion
	}

	if pass == "" {
		return Bundle{}, ErrPassphrase
	}

	h, err := parseHeader(data)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", ErrPassphrase, err)
	}
	salt, _ := base64.StdEncoding.DecodeString(h.Salt)
	nonce, _ := base64.StdEncoding.DecodeString(h.Nonce)
	ct, _ := base64.StdEncoding.DecodeString(h.Data)

	key := pbkdf2SHA256([]byte(pass), salt, h.Iterations, keyLen)
	gcm, err := newGCM(key)
	if err != nil {
		return Bundle{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := gcm.Open(nil, nonce, ct, h.aad())
	if err != nil {
		return Bundle{}, ErrPassphrase
	}

	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("备份内容损坏: %w", err)
	}
	if b.Version > bundleVersion {
		return Bundle{}, ErrNewerVersion
	}
	return b, nil
}

// headerKeys are the only keys a header may carry, each exactly once.
var headerKeys = map[string]bool{
	"format": true, "version": true, "kdf": true, "iterations": true,
	"salt": true, "nonce": true, "data": true,
}

// parseHeader decodes and validates the header of a backup whose format and
// version have already been checked. Any problem is a tampered header.
func parseHeader(data []byte) (header, error) {
	if err := checkHeaderKeys(data); err != nil {
		return header{}, err
	}
	var h header
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return header{}, err
	}
	switch {
	case h.Format != formatName:
		return header{}, errors.New("头部格式无效")
	case h.Version != formatVersion:
		return header{}, errors.New("头部版本无效")
	case h.KDF != kdfName:
		return header{}, errors.New("密钥派生方式不支持")
	case h.Iterations <= minIterations || h.Iterations > maxIterations:
		return header{}, errors.New("迭代次数无效")
	}
	salt, err := base64.StdEncoding.DecodeString(h.Salt)
	if err != nil || len(salt) < saltLen {
		return header{}, errors.New("盐无效")
	}
	if _, err := base64.StdEncoding.DecodeString(h.Nonce); err != nil {
		return header{}, errors.New("随机数无效")
	}
	if _, err := base64.StdEncoding.DecodeString(h.Data); err != nil {
		return header{}, errors.New("密文无效")
	}
	return h, nil
}

// checkHeaderKeys requires the top-level object to hold exactly the header
// keys, with no duplicates and no case variants. encoding/json matches keys
// case-insensitively and keeps the last duplicate, so this check is needed
// to make every header byte count.
func checkHeaderKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("头部不是对象")
	}
	seen := make(map[string]bool, len(headerKeys))
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok || !headerKeys[key] || seen[key] {
			return errors.New("头部字段无效")
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	if len(seen) != len(headerKeys) {
		return errors.New("头部字段缺失")
	}
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化加密: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化加密: %w", err)
	}
	return gcm, nil
}

// pbkdf2SHA256 derives keyLen bytes from password and salt, as in RFC 8018,
// with HMAC-SHA256 as the PRF.
func pbkdf2SHA256(password, salt []byte, iter, size int) []byte {
	const hashLen = sha256.Size
	blocks := (size + hashLen - 1) / hashLen
	prf := hmac.New(sha256.New, password)
	out := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		prf.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:size]
}
