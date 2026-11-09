package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// KeyEntry is an extra API key kept beside a provider's current one. The
// current key lives in Provider.Key (named by KeyName, limited by KeyProtocol);
// the rest are here, in the order they are tried.
type KeyEntry struct {
	Key      string   `json:"key"`
	Name     string   `json:"name,omitempty"`
	Protocol Protocol `json:"protocol,omitempty"` // "" means any protocol the vendor serves
	Off      bool     `json:"off,omitempty"`      // a disabled key is never tried
}

var (
	errLastKey = errors.New("a provider needs at least one enabled key; this change would leave none")
	errNoKey   = errors.New("no key of this provider matches that reference")
)

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in
// lowercase hex. It names a key without showing it.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProtocol is true for "" (any) and the three protocols a key can be
// limited to.
func validKeyProtocol(proto Protocol) bool {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

// keyRing lists the provider's keys in order: the current one first, then the
// extra keys as stored.
func keyRing(p Provider) []KeyEntry {
	var out []KeyEntry
	if p.Key != "" {
		out = append(out, KeyEntry{Key: p.Key, Name: p.KeyName, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeyRing writes an ordered key list back into the provider. The first
// enabled key becomes the current one; keys ahead of it (all disabled) stay
// in the extra keys, at the front. It changes nothing when no key is enabled.
func setKeyRing(p *Provider, ring []KeyEntry) error {
	i := -1
	for j, k := range ring {
		if !k.Off {
			i = j
			break
		}
	}
	if i < 0 {
		return errLastKey
	}
	top := ring[i]
	rest := make([]KeyEntry, 0, len(ring)-1)
	rest = append(rest, ring[:i]...)
	rest = append(rest, ring[i+1:]...)
	p.Key, p.KeyName, p.KeyProtocol = top.Key, top.Name, top.Protocol
	p.Keys = nil
	if len(rest) > 0 {
		p.Keys = rest
	}
	return nil
}

func findKey(ring []KeyEntry, ref string) int {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, k := range ring {
		if KeyRef(k.Key) == ref {
			return i
		}
	}
	return -1
}

// editKeys applies change to one stored provider and saves the file. The
// change works on a copy, so an error leaves the file as it was.
func editKeys(id string, change func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if err := change(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; its sign-in holds the key, so it has no keys to manage", id)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. The new key is enabled and goes after the
// existing ones; a provider with no key gets this one as its current key.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown protocol %q: use chat, responses or anthropic, or leave it empty", proto)
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		for _, k := range ring {
			if k.Key == key {
				return errors.New("that key is already on this provider")
			}
		}
		ring = append(ring, KeyEntry{Key: key, Name: name, Protocol: proto})
		return setKeyRing(p, ring)
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled, right
// after it; the other keys and their protocol limits are kept.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		i := findKey(ring, keyRef)
		if i < 0 {
			return errNoKey
		}
		chosen := ring[i]
		chosen.Off = false
		next := make([]KeyEntry, 0, len(ring))
		next = append(next, chosen)
		next = append(next, ring[:i]...)
		next = append(next, ring[i+1:]...)
		return setKeyRing(p, next)
	})
}

// SetKeyOn enables or disables a key. Disabling the current key hands the
// current place to the next enabled key; the disabled one is kept. Refused,
// with nothing changed, when no other key is enabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		i := findKey(ring, keyRef)
		if i < 0 {
			return errNoKey
		}
		ring[i].Off = !on
		return setKeyRing(p, ring)
	})
}

// RemoveKey deletes a key. The last enabled key cannot be removed; removing
// the current key hands the current place to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		i := findKey(ring, keyRef)
		if i < 0 {
			return errNoKey
		}
		next := make([]KeyEntry, 0, len(ring)-1)
		next = append(next, ring[:i]...)
		next = append(next, ring[i+1:]...)
		return setKeyRing(p, next)
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		i := findKey(ring, keyRef)
		if i < 0 {
			return errNoKey
		}
		ring[i].Name = name
		return setKeyRing(p, ring)
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown protocol %q: use chat, responses or anthropic, or leave it empty", proto)
	}
	return editKeys(id, func(p *Provider) error {
		ring := keyRing(*p)
		i := findKey(ring, keyRef)
		if i < 0 {
			return errNoKey
		}
		ring[i].Protocol = proto
		return setKeyRing(p, ring)
	})
}

// checkKeys refuses a provider whose keys cannot be used as stored.
func checkKeys(p Provider) error {
	if !validKeyProtocol(p.KeyProtocol) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses or anthropic, or leave it empty", p.KeyProtocol)
	}
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	for _, k := range p.Keys {
		if !validKeyProtocol(k.Protocol) {
			return fmt.Errorf("unknown protocol %q on key %s: use chat, responses or anthropic, or leave it empty", k.Protocol, KeyRef(k.Key))
		}
		if seen[k.Key] {
			return errors.New("a provider lists the same key twice")
		}
		seen[k.Key] = true
	}
	return nil
}

// cleanKeys trims the extra keys and drops empty ones.
func cleanKeys(ks []KeyEntry) []KeyEntry {
	var out []KeyEntry
	for _, k := range ks {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// KeyLabel names the key a provider carries for display: its name, else a
// masked form of the key itself.
func (p Provider) KeyLabel() string {
	if p.Account != nil {
		return p.Account.User
	}
	if p.KeyName != "" {
		return p.KeyName
	}
	return Mask(p.Key)
}

// withKey is a copy of the provider that carries only this key.
func (p Provider) withKey(k KeyEntry) Provider {
	c := p
	c.Key, c.KeyName, c.KeyProtocol = k.Key, k.Name, k.Protocol
	c.Keys = nil
	return c
}

// hasKeys is false for providers that need no key, such as local servers, and
// for signed-in accounts.
func (p Provider) hasKeys() bool {
	return p.Account == nil && (p.Key != "" || len(p.Keys) > 0)
}

// KeyFor returns the copy of the provider to talk to one protocol with: its
// first enabled key that may speak that protocol. A key limited to another
// protocol is never used there. ok is false when no key fits.
func (p Provider) KeyFor(proto Protocol) (Provider, bool) {
	if !p.hasKeys() {
		return p, true
	}
	for _, k := range keyRing(p) {
		if k.Off || !validKeyProtocol(k.Protocol) {
			continue
		}
		if k.Protocol != "" && k.Protocol != proto {
			continue
		}
		return p.withKey(k), true
	}
	return Provider{}, false
}

// KeyCandidates lists copies of the provider, each with one enabled key, in
// the order to try them for a request from client protocol from, for model.
// Keys limited to a protocol the vendor lacks are left out, and so are
// disabled keys. Claude models try Anthropic keys first; GPT, o-series and
// codex models try OpenAI keys first; other models try a key that speaks the
// client's protocol directly, to avoid translating. A key that fits poorly
// comes last.
func (p Provider) KeyCandidates(from Protocol, model string) []Provider {
	if p.Account != nil {
		// a signed-in login: the agent's current one first, then the saved
		// logins that are on at the same time, in the order they were saved
		self := p
		acct := *p.Account
		acct.also = nil
		self.Account = &acct
		return append([]Provider{self}, p.Account.also...)
	}
	if !p.hasKeys() {
		return []Provider{p}
	}
	type cand struct {
		p    Provider
		rank int
	}
	var cs []cand
	for _, k := range keyRing(p) {
		if k.Off || !validKeyProtocol(k.Protocol) {
			continue
		}
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue // the vendor has no endpoint for this key
		}
		if k.Protocol == "" && len(p.Speaks()) == 0 {
			continue
		}
		cs = append(cs, cand{p.withKey(k), keyRank(k.Protocol, from, p, model)})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].rank < cs[j].rank })
	out := make([]Provider, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.p)
	}
	return out
}

// keyRank orders keys for a request: lower is tried first.
func keyRank(kp, from Protocol, p Provider, model string) int {
	switch modelFamily(model) {
	case "claude":
		switch kp {
		case Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case "openai":
		switch kp {
		case Chat, Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if kp == from || (kp == "" && p.Base(from) != "") {
		return 0 // speaks the client's protocol directly
	}
	return 1
}

// modelFamily sorts a model id into "claude", "openai" (GPT, o-series, codex)
// or "" (anything else). A provider prefix such as "anthropic/" is ignored.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"):
		return "openai"
	case len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9':
		return "openai"
	}
	return ""
}
