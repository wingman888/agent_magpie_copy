package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Key is an additional API key of a provider. The first key is the
// provider's own Key; these follow it.
type Key struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Off      bool     `json:"off,omitempty"` // disabled: never tried
	Protocol Protocol `json:"protocol,omitempty"`
}

// KeyRef is the fingerprint that names a key without spelling it out: the
// first five bytes of its SHA-256, in lowercase hex.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// ValidProtocol reports whether a key may be limited to this protocol
// ("" means any).
func ValidProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

func checkProtocol(p Protocol) error {
	if !ValidProtocol(p) {
		return fmt.Errorf("unknown protocol %q — use chat, responses, anthropic, or leave it empty", string(p))
	}
	return nil
}

// validate rejects what normalize would otherwise have to guess at.
func validate(p Provider) error {
	if err := checkProtocol(p.KeyProtocol); err != nil {
		return err
	}
	for _, k := range p.Keys {
		if err := checkProtocol(k.Protocol); err != nil {
			return err
		}
	}
	if p.Icon = strings.TrimSpace(p.Icon); strings.HasPrefix(p.Icon, "file:") && IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
		return fmt.Errorf("bad icon reference %q", p.Icon)
	}
	return nil
}

// cleanKeys trims the extra keys and drops empty ones and repeats.
func cleanKeys(p Provider) []Key {
	var out []Key
	seen := map[string]bool{strings.TrimSpace(p.Key): true}
	for _, k := range p.Keys {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	return out
}

// cleanHeaders keeps headers for custom providers only, trimmed, without
// nameless entries; an empty map is nil so it is not written.
func cleanHeaders(p Provider) map[string]string {
	if p.Preset != "" {
		return nil
	}
	var out map[string]string
	for k, v := range p.Headers {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// Catalogs lists the models.dev ids the provider's Catalog names, in order.
func (p Provider) Catalogs() []string {
	return cleanList(strings.Split(p.Catalog, ","))
}

// KeyInfo is one key of a provider as seen by callers.
type KeyInfo struct {
	Ref      string
	Name     string
	Key      string
	Off      bool
	Protocol Protocol
}

// KeyList lists all keys, the current one first.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{Ref: KeyRef(p.Key), Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{Ref: KeyRef(k.Key), Name: k.Name, Key: k.Key, Off: k.Off, Protocol: k.Protocol})
	}
	return out
}

// mutate edits one stored (non-account) provider and saves the file.
func mutate(id string, fn func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break
		}
		if err := fn(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q with keys", id)
}

// index finds a key by fingerprint: -1 for the current key, else its place
// in Keys.
func (p Provider) keyIndex(ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref != "" {
		if p.Key != "" && KeyRef(p.Key) == ref {
			return -1, nil
		}
		for i, k := range p.Keys {
			if KeyRef(k.Key) == ref {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("%s has no key %q", p.ID, ref)
}

func (p Provider) enabledExtra() int {
	for i, k := range p.Keys {
		if !k.Off {
			return i
		}
	}
	return -1
}

// AddKey appends a key. The first key of a provider becomes its current one.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("empty key")
	}
	if err := checkProtocol(proto); err != nil {
		return err
	}
	return mutate(id, func(p *Provider) error {
		for _, k := range p.KeyList() {
			if k.Key == key {
				return fmt.Errorf("%s already has that key (%s)", p.ID, k.Ref)
			}
		}
		name = strings.TrimSpace(name)
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, Key{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled,
// right behind it; names and protocol limits travel with their keys.
func UseKey(id, keyRef string) error {
	return mutate(id, func(p *Provider) error {
		i, err := p.keyIndex(keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			return nil
		}
		next := p.Keys[i]
		old := Key{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
		rest := append([]Key{old}, p.Keys[:i]...)
		rest = append(rest, p.Keys[i+1:]...)
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = rest
		return nil
	})
}

// SetKeyOn enables or disables a key. Disabling the current key hands over
// to the next enabled one; the last enabled key cannot be disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return mutate(id, func(p *Provider) error {
		i, err := p.keyIndex(keyRef)
		if err != nil {
			return err
		}
		if i >= 0 {
			p.Keys[i].Off = !on
			return nil
		}
		if on {
			return nil
		}
		j := p.enabledExtra()
		if j < 0 {
			return errors.New("that is the last enabled key; enable another before turning it off")
		}
		next := p.Keys[j]
		p.Keys[j] = Key{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		return nil
	})
}

// RemoveKey deletes a key; the last enabled key cannot be deleted.
func RemoveKey(id, keyRef string) error {
	return mutate(id, func(p *Provider) error {
		i, err := p.keyIndex(keyRef)
		if err != nil {
			return err
		}
		if i >= 0 {
			p.Keys = append(p.Keys[:i:i], p.Keys[i+1:]...)
			return nil
		}
		j := p.enabledExtra()
		if j < 0 {
			return errors.New("that is the last enabled key; add another before deleting it")
		}
		next := p.Keys[j]
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = append(p.Keys[:j:j], p.Keys[j+1:]...)
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	return mutate(id, func(p *Provider) error {
		i, err := p.keyIndex(keyRef)
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if i < 0 {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if err := checkProtocol(proto); err != nil {
		return err
	}
	return mutate(id, func(p *Provider) error {
		i, err := p.keyIndex(keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}
