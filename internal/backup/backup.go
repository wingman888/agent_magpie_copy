// Package backup moves a magpie setup to another machine: providers (with
// their keys unless asked otherwise), their pictures, settings, profiles and
// what each agent uses now, sealed in one file under a passphrase.
//
// Subscriptions are not in it: they are the agents' own sign-ins, and each
// machine signs in for itself.
package backup

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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Ext is the extension a backup file carries by default.
const Ext = ".magpie-backup"

const (
	format     = "magpie-backup"
	kdfName    = "pbkdf2-sha256"
	iterations = 600000
	minIter    = 100000
	maxIter    = 10000000
	saltLen    = 16
)

// ErrPassphrase is what Open says when the passphrase is wrong or the file
// was changed: AES-GCM cannot tell the two apart.
var ErrPassphrase = errors.New("wrong passphrase, or the file was changed")

var (
	errNotBackup = errors.New("not a magpie backup")
	errNewer     = errors.New("this backup was made by a newer magpie; update magpie to open it")
)

// Bundle is what a backup holds.
type Bundle struct {
	Version   int                        `json:"version"`
	Created   time.Time                  `json:"created"`
	App       string                     `json:"app,omitempty"` // the magpie that made it
	Keys      bool                       `json:"keys"`
	Providers []provider.Provider        `json:"providers,omitempty"`
	Icons     map[string][]byte          `json:"icons,omitempty"` // by file name
	Settings  *settings.Settings         `json:"settings,omitempty"`
	Profiles  map[string]profile.Profile `json:"profiles,omitempty"`
	Agents    profile.Profile            `json:"agents,omitempty"`
}

var secretHeader = regexp.MustCompile(`(?i)auth|key|token|secret|cookie|session|password`)

// Collect gathers the backup from this machine. Without keys, no provider
// keeps a key and no header that looks like a credential is left.
func Collect(keys bool, app string) (Bundle, error) {
	b := Bundle{Version: 1, Created: time.Now().UTC(), App: app, Keys: keys}
	for _, p := range provider.Stored() {
		if !keys {
			p.Key, p.KeyName, p.Keys, p.KeyProtocol = "", "", nil, ""
			if p.Headers != nil {
				h := map[string]string{}
				for k, v := range p.Headers {
					if !secretHeader.MatchString(k) {
						h[k] = v
					}
				}
				p.Headers = h
				if len(h) == 0 {
					p.Headers = nil
				}
			}
		}
		p.IconURL = ""
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			if path := provider.IconFile(name); path != "" {
				if data, err := readIcon(path); err == nil {
					if b.Icons == nil {
						b.Icons = map[string][]byte{}
					}
					b.Icons[name] = data
				}
			}
		}
		b.Providers = append(b.Providers, p)
	}
	if st, ok := loadSettings(); ok {
		b.Settings = &st
	}
	ps, err := profile.Load()
	if err != nil {
		return Bundle{}, err
	}
	if len(ps) > 0 {
		b.Profiles = ps
	}
	b.Agents = profile.Snapshot()
	return b, nil
}

// aad is what the envelope header contributes to the seal, so none of it can
// be changed without the passphrase.
func aad(f, kdf string, version, iter int, salt []byte) []byte {
	return []byte(fmt.Sprintf("%s|%d|%s|%d|%s", f, version, kdf, iter, base64.StdEncoding.EncodeToString(salt)))
}

type envelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func gcm(pass string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Seal encrypts the bundle under pass into a JSON file.
func Seal(b Bundle, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("the passphrase is empty")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	g, err := gcm(pass, salt, iterations)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	e := envelope{Format: format, Version: 1, KDF: kdfName, Iterations: iterations, Salt: salt, Nonce: nonce}
	e.Ciphertext = g.Seal(nil, nonce, plain, aad(e.Format, e.KDF, e.Version, e.Iterations, salt))
	out, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Open decrypts a backup file.
func Open(data []byte, pass string) (Bundle, error) {
	var e envelope
	if json.Unmarshal(data, &e) != nil || e.Format != format {
		return Bundle{}, errNotBackup
	}
	if e.Version != 1 || e.KDF != kdfName {
		return Bundle{}, errNewer
	}
	if e.Iterations < minIter || e.Iterations > maxIter || len(e.Salt) < saltLen || len(e.Ciphertext) == 0 {
		return Bundle{}, errNotBackup
	}
	g, err := gcm(pass, e.Salt, e.Iterations)
	if err != nil {
		return Bundle{}, err
	}
	if len(e.Nonce) != g.NonceSize() {
		return Bundle{}, errNotBackup
	}
	plain, err := g.Open(nil, e.Nonce, e.Ciphertext, aad(e.Format, e.KDF, e.Version, e.Iterations, e.Salt))
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	var b Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return Bundle{}, errNotBackup
	}
	return b, nil
}

// Parts picks what Restore brings back.
type Parts struct{ Providers, Settings, Profiles, Agents bool }

// All restores everything.
var All = Parts{true, true, true, true}

// Result is what Restore did.
type Result struct {
	Added, Replaced int      // providers
	NeedKey         []string // names of restored providers still without a key
	Settings        bool
	Profiles        int
	Agents          int      // agent fields changed
	Skipped         []string // agent fields left as they were
}

// Restore applies the bundle: providers first, so that models resolved
// through them take, then settings, profiles and the agents' models.
func Restore(b Bundle, parts Parts) (Result, error) {
	var r Result
	if parts.Providers {
		for name, data := range b.Icons {
			if err := provider.RestoreIcon(name, data); err != nil {
				return r, err
			}
		}
		var err error
		if r.Added, r.Replaced, err = provider.Restore(b.Providers); err != nil {
			return r, err
		}
		for _, p := range b.Providers {
			if q, err := provider.Find(p.ID); err == nil && !q.Ready() {
				r.NeedKey = append(r.NeedKey, q.Name)
			}
		}
	}
	if parts.Settings && b.Settings != nil {
		if err := settings.Save(*b.Settings); err != nil {
			return r, err
		}
		r.Settings = true
	}
	if parts.Profiles {
		for _, name := range profile.Names(profileMap(b.Profiles)) {
			if err := profile.Save(name, b.Profiles[name]); err != nil {
				return r, err
			}
			r.Profiles++
		}
	}
	if parts.Agents {
		r.Agents, r.Skipped = applyAgents(b.Agents)
	}
	return r, nil
}

func profileMap(m map[string]profile.Profile) map[string]profile.Profile { return m }

// applyAgents sets each field on the agents installed here, one at a time,
// so one that cannot be set does not hold back the others.
func applyAgents(p profile.Profile) (int, []string) {
	detected := map[string]*agent.Agent{}
	for _, a := range agent.Detected() {
		detected[a.ID] = a
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		switch {
		case strings.HasSuffix(k, ".provider"):
			return 0
		case strings.HasSuffix(k, ".model"):
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	changed := 0
	var skipped []string
	for _, k := range keys {
		id, field, _ := strings.Cut(k, ".")
		a := detected[id]
		if a == nil {
			skipped = append(skipped, k)
			continue
		}
		f := a.Field(field)
		if f == nil {
			skipped = append(skipped, k)
			continue
		}
		if f.Get() == p[k] {
			continue
		}
		if err := f.Set(p[k]); err != nil {
			skipped = append(skipped, k)
			continue
		}
		changed++
	}
	return changed, skipped
}
