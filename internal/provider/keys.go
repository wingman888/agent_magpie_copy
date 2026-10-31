package provider

// Several API keys (accounts) per provider. The front key lives in the
// provider's `key` field (and `keyName`), so everything that reads Key keeps
// working; the others sit in `keys`, each possibly switched off. The front
// key is always enabled. A config without keyName/keys is a one-key provider.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is a key behind the front one.
type ExtraKey struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"` // kept, but never used
}

// KeyInfo describes one key without revealing it.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the front key
	On     bool   `json:"on"`     // enabled
}

// KeyOn is an enabled key, as the gateway tries them.
type KeyOn struct {
	ID   string
	Name string
	Key  string
}

// KeyID is the stable short id of a key, derived from its content.
func KeyID(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:8]
}

type entry struct {
	name, key string
	on        bool
}

func (p Provider) entries() []entry {
	var out []entry
	if p.Key != "" {
		out = append(out, entry{p.KeyName, p.Key, true})
	}
	for _, k := range p.Keys {
		out = append(out, entry{k.Name, k.Key, !k.Off})
	}
	return out
}

// setEntries writes the list back; the first entry must be enabled.
func (p *Provider) setEntries(es []entry) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	for i, e := range es {
		if i == 0 {
			p.Key, p.KeyName = e.key, e.name
			continue
		}
		p.Keys = append(p.Keys, ExtraKey{Name: e.name, Key: e.key, Off: !e.on})
	}
}

// KeyList describes every key in the order they are tried.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, e := range p.entries() {
		out = append(out, KeyInfo{ID: KeyID(e.key), Name: e.name, Masked: Mask(e.key), Active: i == 0, On: e.on})
	}
	return out
}

// KeysOn lists the enabled keys in the order they are tried.
func (p Provider) KeysOn() []KeyOn {
	var out []KeyOn
	for _, e := range p.entries() {
		if e.on {
			out = append(out, KeyOn{ID: KeyID(e.key), Name: e.name, Key: e.key})
		}
	}
	return out
}

// normalizeKeys trims, drops empty and duplicate keys, and makes sure the
// front key exists and is enabled.
func normalizeKeys(p *Provider) {
	p.Key = strings.TrimSpace(p.Key)
	p.KeyName = strings.TrimSpace(p.KeyName)
	var es []entry
	seen := map[string]bool{}
	if p.Key != "" {
		es = append(es, entry{p.KeyName, p.Key, true})
		seen[p.Key] = true
	}
	for _, k := range p.Keys {
		key := strings.TrimSpace(k.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		es = append(es, entry{strings.TrimSpace(k.Name), key, !k.Off})
	}
	if len(es) > 0 && !es[0].on {
		for i, e := range es {
			if e.on {
				es = promote(es, i)
				break
			}
		}
		if !es[0].on {
			es[0].on = true
		}
	}
	p.setEntries(es)
}

// promote moves es[i] to the front, the rest keeping their order.
func promote(es []entry, i int) []entry {
	out := make([]entry, 0, len(es))
	out = append(out, es[i])
	out = append(out, es[:i]...)
	return append(out, es[i+1:]...)
}

func countOn(es []entry) int {
	n := 0
	for _, e := range es {
		if e.on {
			n++
		}
	}
	return n
}

func indexOf(es []entry, ref string) int {
	ref = strings.TrimSpace(ref)
	for i, e := range es {
		if ref != "" && KeyID(e.key) == ref {
			return i
		}
	}
	return -1
}

// editKeys loads the stored provider, lets fn change its key list, and saves.
func editKeys(id string, fn func(es []entry) ([]entry, error)) error {
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
		normalizeKeys(&p)
		es, err := fn(p.entries())
		if err != nil {
			return err
		}
		p.setEntries(es)
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

var errNoKey = errors.New("no such key")

// AddKey adds an enabled key behind the existing ones.
func AddKey(id, name, key string) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("the API key is empty")
	}
	return editKeys(id, func(es []entry) ([]entry, error) {
		for _, e := range es {
			if e.key == key {
				return nil, errors.New("that key is already on this provider")
			}
		}
		return append(es, entry{name, key, true}), nil
	})
}

// UseKey makes a key the front one (enabling it); the old front follows it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(es []entry) ([]entry, error) {
		i := indexOf(es, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		if i == 0 {
			return es, nil
		}
		es[i].on = true
		return promote(es, i), nil
	})
}

// SetKeyOn enables or disables a key; at least one stays enabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(es []entry) ([]entry, error) {
		i := indexOf(es, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		if es[i].on == on {
			return es, nil
		}
		if on {
			es[i].on = true
			return es, nil
		}
		if countOn(es) <= 1 {
			return nil, errors.New("at least one key must stay enabled")
		}
		es[i].on = false
		if i == 0 {
			for j := 1; j < len(es); j++ {
				if es[j].on {
					return promote(es, j), nil
				}
			}
		}
		return es, nil
	})
}

// RemoveKey deletes a key; at least one enabled key must remain.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(es []entry) ([]entry, error) {
		i := indexOf(es, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		if es[i].on && countOn(es) <= 1 {
			return nil, errors.New("at least one key must stay enabled")
		}
		rest := append(append([]entry{}, es[:i]...), es[i+1:]...)
		if i == 0 {
			for j := range rest {
				if rest[j].on {
					rest = promote(rest, j)
					break
				}
			}
		}
		return rest, nil
	})
}

// RenameKey names a key; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, func(es []entry) ([]entry, error) {
		i := indexOf(es, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		es[i].name = strings.TrimSpace(name)
		return es, nil
	})
}
