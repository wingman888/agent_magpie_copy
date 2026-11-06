package provider

// Keys: one provider may hold several API keys. The current key is the
// provider's own Key (with KeyName and KeyProtocol); the others sit in Keys in
// the order they were added, each possibly switched off. A key limited to one
// protocol only ever reaches that protocol's endpoint.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyEntry is an additional API key of a provider.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" is any protocol
	Off      bool     `json:"off,omitempty"`
}

// KeyChoice is one key of a provider, as the gateway sees it.
type KeyChoice struct {
	Ref      string   // fingerprint, see KeyRef
	Name     string   // "" when unnamed
	Key      string   // the secret
	Protocol Protocol // "" is any protocol
	Off      bool
	Current  bool // the provider's own key
}

// ValidKeyProtocol reports whether a key may be limited to proto. Only the
// three wire APIs can be named; "" means any.
func ValidKeyProtocol(proto Protocol) bool {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

// KeyRef is the fingerprint users and the rest of magpie name a key by: the
// first five bytes of its SHA-256, as ten lowercase hex digits.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// KeyList lists the provider's keys: the current one first, then the others
// in order. A keyless provider has none.
func (p Provider) KeyList() []KeyChoice {
	var out []KeyChoice
	if p.Key != "" {
		out = append(out, KeyChoice{Ref: KeyRef(p.Key), Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Current: true})
	}
	for _, e := range p.Keys {
		out = append(out, KeyChoice{Ref: KeyRef(e.Key), Name: e.Name, Key: e.Key, Protocol: e.Protocol, Off: e.Off})
	}
	return out
}

// normalizeKeys trims the key fields, drops empty and repeated keys, and
// makes sure a provider with keys always has a current one.
func normalizeKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	p.KeyProtocol = Protocol(strings.TrimSpace(string(p.KeyProtocol)))
	seen := map[string]bool{p.Key: true}
	var keys []KeyEntry
	for _, e := range p.Keys {
		e.Key = strings.TrimSpace(e.Key)
		e.Name = strings.TrimSpace(e.Name)
		e.Protocol = Protocol(strings.TrimSpace(string(e.Protocol)))
		if e.Key == "" || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		keys = append(keys, e)
	}
	p.Keys = keys
	if p.Key == "" && len(p.Keys) > 0 {
		first := p.Keys[0]
		p.Key, p.KeyName, p.KeyProtocol = first.Key, first.Name, first.Protocol
		p.Keys = p.Keys[1:]
	}
	if len(p.Keys) == 0 {
		p.Keys = nil
	}
	return p
}

// checkKeys rejects protocol limits magpie does not know.
func checkKeys(p Provider) error {
	if !ValidKeyProtocol(p.KeyProtocol) {
		return fmt.Errorf("key protocol %q: use chat, responses, anthropic or leave it empty", p.KeyProtocol)
	}
	for _, e := range p.Keys {
		if !ValidKeyProtocol(e.Protocol) {
			return fmt.Errorf("key protocol %q: use chat, responses, anthropic or leave it empty", e.Protocol)
		}
	}
	return nil
}

// carryKeys keeps the stored keys when a provider is saved again without
// them: the same current key keeps its names and the other keys; a new
// current key replaces the old one and the others stay.
func carryKeys(old, p Provider) Provider {
	if old.Key == "" && len(old.Keys) == 0 {
		return p
	}
	if p.Key != "" && p.Key == old.Key {
		p.KeyName, p.KeyProtocol, p.Keys = old.KeyName, old.KeyProtocol, old.Keys
		return normalizeKeys(p)
	}
	p.KeyName, p.KeyProtocol, p.Keys = "", "", nil
	for _, e := range old.Keys {
		if e.Key != p.Key {
			p.Keys = append(p.Keys, e)
		}
	}
	return normalizeKeys(p)
}

// locate finds the key a reference names: the current key (top) or an index
// into Keys.
func (p *Provider) locate(ref string) (top bool, idx int, err error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return true, -1, nil
	}
	for i, e := range p.Keys {
		if KeyRef(e.Key) == ref {
			return false, i, nil
		}
	}
	return false, -1, fmt.Errorf("no key %q on %s — magpie providers lists them", ref, p.ID)
}

// firstEnabled is the index in Keys of the first key that is switched on, or -1.
func (p *Provider) firstEnabled() int {
	for i, e := range p.Keys {
		if !e.Off {
			return i
		}
	}
	return -1
}

// mutateKeys applies fn to a stored API-key provider and saves the result.
// Nothing is saved when fn fails.
func mutateKeys(id string, fn func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it has no API keys", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break
		}
		p.Key = strings.TrimSpace(p.Key)
		p = normalizeKeys(p)
		if err := fn(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds an API key to a provider. The first key becomes the current
// one; later keys are enabled and go after the existing ones.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !ValidKeyProtocol(proto) {
		return fmt.Errorf("key protocol %q: use chat, responses, anthropic or leave it empty", proto)
	}
	return mutateKeys(id, func(p *Provider) error {
		if p.Key == key {
			return fmt.Errorf("that key is already on %s", p.ID)
		}
		for _, e := range p.Keys {
			if e.Key == key {
				return fmt.Errorf("that key is already on %s", p.ID)
			}
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, KeyEntry{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled, right
// after the new current key; names and protocol limits travel with each key.
func UseKey(id, keyRef string) error {
	return mutateKeys(id, func(p *Provider) error {
		top, i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if top {
			return nil
		}
		next := p.Keys[i]
		rest := []KeyEntry{{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}}
		for j, e := range p.Keys {
			if j != i {
				rest = append(rest, e)
			}
		}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = rest
		return nil
	})
}

// SetKeyOn switches a key on or off. Switching off the current key hands the
// current place to the next enabled key; the old one is kept, switched off.
// Magpie always keeps one enabled key.
func SetKeyOn(id, keyRef string, on bool) error {
	return mutateKeys(id, func(p *Provider) error {
		top, i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if !top {
			p.Keys[i].Off = !on
			return nil
		}
		if on {
			return nil
		}
		n := p.firstEnabled()
		if n < 0 {
			return fmt.Errorf("%s would have no enabled key left", p.ID)
		}
		next := p.Keys[n]
		rest := []KeyEntry{{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}}
		for j, e := range p.Keys {
			if j != n {
				rest = append(rest, e)
			}
		}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = rest
		return nil
	})
}

// RemoveKey deletes a key. Removing the current key hands the current place to
// the next enabled key. The last enabled key cannot be removed.
func RemoveKey(id, keyRef string) error {
	return mutateKeys(id, func(p *Provider) error {
		top, i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if top {
			n := p.firstEnabled()
			if n < 0 {
				return fmt.Errorf("%s would have no enabled key left", p.ID)
			}
			next := p.Keys[n]
			p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
			var rest []KeyEntry
			for j, e := range p.Keys {
				if j != n {
					rest = append(rest, e)
				}
			}
			p.Keys = rest
			return nil
		}
		// the current key is always enabled, so removing any other key
		// never leaves the provider without one
		var rest []KeyEntry
		for j, e := range p.Keys {
			if j != i {
				rest = append(rest, e)
			}
		}
		p.Keys = rest
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return mutateKeys(id, func(p *Provider) error {
		top, i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if top {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !ValidKeyProtocol(proto) {
		return fmt.Errorf("key protocol %q: use chat, responses, anthropic or leave it empty", proto)
	}
	return mutateKeys(id, func(p *Provider) error {
		top, i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if top {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}
