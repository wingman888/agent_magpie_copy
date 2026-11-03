package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is an API key a custom or preset vendor holds besides the current
// one. Keys are tried in the order they were added, after the current key.
// The current key itself lives in Provider.Key, named by Provider.KeyName and
// limited by Provider.KeyProtocol.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Off      bool     `json:"off,omitempty"`      // kept but never tried
	Protocol Protocol `json:"protocol,omitempty"` // empty: usable on any endpoint
}

// KeyInfo describes one key for display. Ref is the key's fingerprint (see
// KeyRef), the only handle the key functions take.
type KeyInfo struct {
	Ref      string
	Name     string
	Masked   string
	Protocol Protocol
	Current  bool
	Off      bool
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in
// lowercase hex. It identifies a key without showing it.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validProtocol reports whether a key may be limited to a protocol. Gemini is
// only ever served to clients, so it is not a key limit.
func validProtocol(pr Protocol) bool {
	switch pr {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

// KeyList lists the current key first, then the others in order.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{Ref: KeyRef(p.Key), Name: p.KeyName, Masked: Mask(p.Key), Protocol: p.KeyProtocol, Current: true})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{Ref: KeyRef(k.Key), Name: k.Name, Masked: Mask(k.Key), Protocol: k.Protocol, Off: k.Off})
	}
	return out
}

// KeyCandidates are copies of the provider, one per enabled key, in the order
// magpie tries them: the current key, then the enabled others as added. A
// provider with no key of its own, or a signed-in account, is its own only
// candidate.
//
// A key limited to a protocol only reaches that protocol's endpoint: its copy
// keeps just that base URL. A key whose protocol the provider lacks is left
// out, since it has nowhere to be sent.
func (p Provider) KeyCandidates() []Provider {
	if p.Account != nil {
		return []Provider{p}
	}
	var out []Provider
	if p.Key != "" {
		if c, ok := p.keyCopy(p.Key, p.KeyName, p.KeyProtocol); ok {
			out = append(out, c)
		}
	}
	for _, k := range p.Keys {
		if k.Off || k.Key == "" {
			continue
		}
		if c, ok := p.keyCopy(k.Key, k.Name, k.Protocol); ok {
			out = append(out, c)
		}
	}
	if p.Key == "" && len(p.Keys) == 0 {
		return []Provider{p} // no key at all: a local server that needs none
	}
	return out
}

// keyCopy is p as it is sent with one key. ok is false when the key cannot
// be used with this provider: its protocol limit is unknown, or the provider
// has no endpoint for that protocol.
func (p Provider) keyCopy(key, name string, proto Protocol) (Provider, bool) {
	c := p
	c.Key, c.KeyName, c.KeyProtocol = key, name, proto
	c.Keys = nil
	if proto == "" {
		return c, true
	}
	if !validProtocol(proto) {
		return c, false
	}
	base := p.Base(proto)
	if base == "" {
		return c, false
	}
	c.Chat, c.Responses, c.Anthropic = "", "", ""
	switch proto {
	case Chat:
		c.Chat = base
	case Responses:
		c.Responses = base
	case Anthropic:
		c.Anthropic = base
	}
	return c, true
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit when proto
// is empty. The limit travels with the key: the current key's is KeyProtocol.
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validProtocol(proto) {
		return unknownProtocol(proto)
	}
	return keyEdit(id, func(p *Provider) error {
		i := p.locate(keyRef)
		switch {
		case i == -1:
			p.KeyProtocol = proto
		case i >= 0:
			p.Keys[i].Protocol = proto
		default:
			return noKey(keyRef, p.Name)
		}
		return nil
	})
}

// AddKey appends a key to a provider. It is enabled and goes after the keys
// already there. A provider with no key yet gets this one as its current key.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("a key is needed")
	}
	if !validProtocol(proto) {
		return unknownProtocol(proto)
	}
	pr, err := Find(id)
	if err != nil {
		return err
	}
	if pr.Account != nil {
		return fmt.Errorf("%s is a signed-in account; it has no keys to add", pr.Name)
	}
	name = strings.TrimSpace(name)
	return editStored(pr.ID, func(p *Provider) error {
		if p.locate(KeyRef(key)) != -2 {
			return fmt.Errorf("%s already has that key", p.Name)
		}
		if p.Key == "" {
			p.setTop(ExtraKey{Name: name, Key: key, Protocol: proto})
			return nil
		}
		p.Keys = append(p.Keys, ExtraKey{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled and
// goes first among the others. Both keep their names and protocol limits.
func UseKey(id, keyRef string) error {
	return keyEdit(id, func(p *Provider) error {
		i := p.locate(keyRef)
		if i == -1 {
			return nil // already the current key
		}
		if i < 0 {
			return noKey(keyRef, p.Name)
		}
		next := p.Keys[i]
		next.Off = false
		p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
		if old := p.top(); old.Key != "" {
			p.Keys = append([]ExtraKey{old}, p.Keys...)
		}
		p.setTop(next)
		return nil
	})
}

// SetKeyOn enables or disables a key. A key that is off is never tried.
// Turning off the current key hands the role to the next enabled key, which
// leaves the old one kept but off. A provider must keep one enabled key.
func SetKeyOn(id, keyRef string, on bool) error {
	return keyEdit(id, func(p *Provider) error {
		i := p.locate(keyRef)
		if i == -1 {
			if on {
				return nil
			}
			j := p.firstEnabled()
			if j < 0 {
				return lastKey(p.Name)
			}
			next := p.Keys[j]
			p.Keys = append(p.Keys[:j], p.Keys[j+1:]...)
			old := p.top()
			old.Off = true
			p.setTop(next)
			p.Keys = append([]ExtraKey{old}, p.Keys...)
			return nil
		}
		if i < 0 {
			return noKey(keyRef, p.Name)
		}
		if !on && !p.Keys[i].Off && p.enabledCount() <= 1 {
			return lastKey(p.Name)
		}
		p.Keys[i].Off = !on
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be removed. Removing
// the current key hands the role to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return keyEdit(id, func(p *Provider) error {
		i := p.locate(keyRef)
		if i == -1 {
			j := p.firstEnabled()
			if j < 0 {
				return lastKey(p.Name)
			}
			p.setTop(p.Keys[j])
			p.Keys = append(p.Keys[:j], p.Keys[j+1:]...)
			return nil
		}
		if i < 0 {
			return noKey(keyRef, p.Name)
		}
		if !p.Keys[i].Off && p.enabledCount() <= 1 {
			return lastKey(p.Name)
		}
		p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
		return nil
	})
}

// RenameKey changes a key's name. The current key's name is the provider's
// keyName.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return keyEdit(id, func(p *Provider) error {
		i := p.locate(keyRef)
		switch {
		case i == -1:
			p.KeyName = name
		case i >= 0:
			p.Keys[i].Name = name
		default:
			return noKey(keyRef, p.Name)
		}
		return nil
	})
}

// keyEdit runs one change to a stored provider's keys, refusing signed-in
// accounts, which keep no keys of magpie's.
func keyEdit(id string, fn func(p *Provider) error) error {
	pr, err := Find(id)
	if err != nil {
		return err
	}
	if pr.Account != nil {
		return fmt.Errorf("%s is a signed-in account; it has no keys", pr.Name)
	}
	return editStored(pr.ID, fn)
}

// editStored applies fn to the stored copy of a provider and saves it.
func editStored(id string, fn func(p *Provider) error) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := normalize(f.Providers[i])
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break // only model picks: an account, not a stored provider
		}
		if err := fn(&p); err != nil {
			return err
		}
		if err := checkKeys(p); err != nil {
			return err
		}
		f.Providers[i] = p
		return storeAndPrune(f)
	}
	return fmt.Errorf("no provider %q — magpie providers lists them", id)
}

// locate finds a key by fingerprint: -1 is the current key, otherwise the
// index in Keys, and -2 when there is no such key.
func (p *Provider) locate(ref string) int {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return -1
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == ref {
			return i
		}
	}
	return -2
}

func (p *Provider) top() ExtraKey {
	return ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
}

func (p *Provider) setTop(k ExtraKey) {
	p.Key, p.KeyName, p.KeyProtocol = k.Key, k.Name, k.Protocol
}

// firstEnabled is the index in Keys of the first enabled key, or -1.
func (p *Provider) firstEnabled() int {
	for i, k := range p.Keys {
		if !k.Off {
			return i
		}
	}
	return -1
}

// enabledCount counts the current key and every enabled extra key.
func (p *Provider) enabledCount() int {
	n := 0
	if p.Key != "" {
		n++
	}
	for _, k := range p.Keys {
		if !k.Off {
			n++
		}
	}
	return n
}

// checkKeys rejects protocol limits magpie does not know and keys that
// appear twice.
func checkKeys(p Provider) error {
	if !validProtocol(p.KeyProtocol) {
		return unknownProtocol(p.KeyProtocol)
	}
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	for _, k := range p.Keys {
		if !validProtocol(k.Protocol) {
			return unknownProtocol(k.Protocol)
		}
		if seen[k.Key] {
			return fmt.Errorf("%s already has that key", p.Name)
		}
		seen[k.Key] = true
	}
	return nil
}

// cleanKeys trims extra keys and drops entries with no key text.
func cleanKeys(ks []ExtraKey) []ExtraKey {
	var out []ExtraKey
	for _, k := range ks {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

func unknownProtocol(pr Protocol) error {
	return fmt.Errorf("unknown protocol %q: use chat, responses or anthropic, or leave it empty for any", pr)
}

func noKey(ref, name string) error {
	return fmt.Errorf("%s has no key %q; magpie providers <id> lists the keys", name, ref)
}

func lastKey(name string) error {
	return fmt.Errorf("%s would have no enabled key left; enable another key first", name)
}
