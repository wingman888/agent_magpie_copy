// Package backup carries a whole magpie setup from one machine to another:
// the providers (keys, extra keys, request headers), the pictures given to
// them, the settings, the saved profiles and every agent's current model.
//
// A backup is one file sealed with a passphrase. Its first line is a plain
// JSON header naming the format and how the key is derived; the rest is the
// setup, encrypted with AES-256-GCM under a key PBKDF2-SHA256 derives from
// the passphrase. The header is the cipher's additional data, so changing
// any of it — the iteration count, say — makes the file fail to open just
// as a wrong passphrase does.
//
// Subscriptions (an agent the user signed in to) never go into a backup:
// their sign-in belongs to the agent on each machine.
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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is a backup file's extension.
const Ext = ".magpie-backup"

const (
	format     = "magpie-backup"
	version    = 1 // the newest file version this magpie opens
	kdfName    = "pbkdf2-sha256"
	iterations = 600_000

	minIterations = 100_000
	maxIterations = 10_000_000 // a header past this is not ours: don't spin on it
	saltLen       = 16
)

var (
	// ErrPassphrase is a file that does not open: the passphrase is wrong,
	// or the file was changed after it was made.
	ErrPassphrase = errors.New("wrong passphrase, or the file was modified (口令错误或文件被改动)")
	// ErrNotBackup is a file that isn't a magpie backup at all.
	ErrNotBackup = errors.New("不是 magpie 备份 (not a magpie backup)")
	// ErrTooNew is a backup a newer magpie made, in a format this one
	// doesn't know yet.
	ErrTooNew = errors.New("made by a newer magpie: 需升级 magpie (update magpie to restore it)")
)

// Bundle is everything a backup carries.
type Bundle struct {
	Version int    `json:"version"`
	App     string `json:"app,omitempty"`     // the magpie that made it
	Created string `json:"created,omitempty"` // RFC 3339
	// Keys says whether the API keys came along.
	Keys bool `json:"keys"`

	Providers []provider.Provider `json:"providers"`
	// Icons are the pictures providers point at ("file:<name>"), by name.
	Icons map[string][]byte `json:"icons,omitempty"`
	// Settings is nil when the machine had no settings file.
	Settings *settings.Settings         `json:"settings,omitempty"`
	Profiles map[string]profile.Profile `json:"profiles,omitempty"`
	Agents   []AgentSetting             `json:"agents,omitempty"`
}

// AgentSetting is one field of one agent: Claude Code's model, Codex's
// reasoning effort.
type AgentSetting struct {
	Agent string `json:"agent"`
	Field string `json:"field"`
	Value string `json:"value"`
}

// Collect gathers this machine's setup. With keys false nothing secret
// comes along: no API key, and no request header that looks like a
// credential. app names the magpie making it.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: version, App: app, Created: time.Now().UTC().Format(time.RFC3339), Keys: keys}
	for _, p := range provider.Stored() {
		// entries without a URL are only the model picks of a signed-in
		// account: a subscription, which stays with its machine
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			continue
		}
		if !provider.ValidID(p.ID) {
			continue
		}
		p.IconURL, p.Account, p.Hidden = "", nil, false
		if !keys {
			p = withoutSecrets(p)
		}
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if path := provider.IconFile(name); path != "" {
				if st, err := os.Stat(path); err == nil && st.Size() > 0 && st.Size() <= provider.MaxIcon {
					if data, err := os.ReadFile(path); err == nil {
						if b.Icons == nil {
							b.Icons = map[string][]byte{}
						}
						b.Icons[name] = data
					}
				}
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		s := settings.Load()
		if !keys {
			s.Proxy = withoutPassword(s.Proxy)
		}
		b.Settings = &s
	}
	ps, err := profile.Load()
	if err != nil {
		return Bundle{}, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}
	for _, a := range agent.Detected() {
		for _, f := range a.Fields {
			if v := f.Get(); v != "" {
				b.Agents = append(b.Agents, AgentSetting{Agent: a.ID, Field: f.Key, Value: v})
			}
		}
	}
	return b, nil
}

// withoutSecrets is p with no key of any kind and no header that looks
// like a credential.
func withoutSecrets(p provider.Provider) provider.Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(p.Headers) > 0 {
		h := map[string]string{}
		for k, v := range p.Headers {
			if !secretHeader(k) {
				h[k] = v
			}
		}
		p.Headers = h
		if len(h) == 0 {
			p.Headers = nil
		}
	}
	return p
}

var secretWords = []string{"auth", "key", "token", "secret", "cookie", "session", "password", "passwd", "credential", "signature"}

// secretHeader reports whether a request header's name says it carries a
// credential: Authorization, X-Api-Key, Cookie, X-Session-Id…
func secretHeader(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// withoutPassword drops the password from a proxy URL.
func withoutPassword(proxy string) string {
	if !strings.Contains(proxy, "@") {
		return proxy
	}
	raw, scheme := proxy, true
	if !strings.Contains(raw, "://") {
		raw, scheme = "http://"+raw, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	u.User = nil
	s := u.String()
	if !scheme {
		s = strings.TrimPrefix(s, "http://")
	}
	return s
}

type header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
}

// Seal encrypts a bundle with a passphrase, returning the file's content.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the passphrase is empty")
	}
	if b.Version == 0 {
		b.Version = version
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := newGCM(pass, salt, iterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding
	head := fmt.Sprintf(`{"format": %q, "version": %d, "kdf": %q, "iterations": %d, "salt": %q, "nonce": %q}`,
		format, version, kdfName, iterations, enc.EncodeToString(salt), enc.EncodeToString(nonce))
	sealed := enc.EncodeToString(gcm.Seal(nil, nonce, plain, []byte(head)))
	var out bytes.Buffer
	out.WriteString(head)
	out.WriteByte('\n')
	for len(sealed) > 76 {
		out.WriteString(sealed[:76])
		out.WriteByte('\n')
		sealed = sealed[76:]
	}
	out.WriteString(sealed)
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func newGCM(pass string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// split parses a file's header, telling a file that isn't a backup and one
// too new from the rest; head is the header line as it is in the file.
func split(data []byte) (h header, head, body []byte, err error) {
	head, body, _ = bytes.Cut(data, []byte("\n"))
	head = bytes.TrimRight(head, "\r")
	var raw map[string]json.RawMessage
	if len(bytes.TrimSpace(head)) == 0 || json.Unmarshal(head, &raw) != nil || raw == nil {
		return h, nil, nil, ErrNotBackup
	}
	var f string
	if json.Unmarshal(raw["format"], &f) != nil || f != format {
		return h, nil, nil, ErrNotBackup
	}
	var v int
	if json.Unmarshal(raw["version"], &v) != nil || v < 1 {
		return h, nil, nil, ErrNotBackup
	}
	if v > version {
		return h, nil, nil, ErrTooNew
	}
	if json.Unmarshal(head, &h) != nil {
		return h, nil, nil, ErrPassphrase
	}
	return h, head, body, nil
}

// Check tells whether data looks like a backup this magpie can open,
// without the passphrase: ErrNotBackup, ErrTooNew or nil.
func Check(data []byte) error {
	_, _, _, err := split(data)
	if errors.Is(err, ErrPassphrase) {
		return nil // a header damaged past the version: the passphrase check says so
	}
	return err
}

// Open decrypts a backup. A file that is not a backup is ErrNotBackup, one
// a newer magpie made is ErrTooNew; a wrong passphrase, or a file changed
// since it was made (its header included), is ErrPassphrase.
func Open(data []byte, pass string) (Bundle, error) {
	h, head, body, err := split(data)
	if err != nil {
		return Bundle{}, err
	}
	enc := base64.StdEncoding
	if h.KDF != kdfName || h.Iterations < minIterations || h.Iterations > maxIterations {
		return Bundle{}, ErrPassphrase
	}
	salt, err := enc.DecodeString(h.Salt)
	if err != nil || len(salt) < saltLen {
		return Bundle{}, ErrPassphrase
	}
	nonce, err := enc.DecodeString(h.Nonce)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	sealed, err := enc.DecodeString(strings.Join(strings.Fields(string(body)), ""))
	if err != nil || len(sealed) == 0 {
		return Bundle{}, ErrPassphrase
	}
	gcm, err := newGCM(pass, salt, h.Iterations)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return Bundle{}, ErrPassphrase
	}
	plain, err := gcm.Open(nil, nonce, sealed, head)
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, fmt.Errorf("the backup's content is unreadable: %v", err)
	}
	if b.Version > version {
		return Bundle{}, ErrTooNew
	}
	return b, nil
}
