package provider

// Keys: a provider may hold several API keys — accounts with the same
// vendor, a personal and a team one, a paid plan and a free tier. They are
// tried in order: Key (the front one) first, then Keys as listed, skipping
// those switched off. The front key is always on, and a provider always
// keeps at least one key on.
//
// In the file, "key" stays the front key, so everything that reads it keeps
// working; "keyName" names it and "keys" lists the others. A config with
// neither is a provider with one key.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is one of a provider's keys after the front one, as stored.
type ExtraKey struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"` // kept, but never used
}

// KeyInfo describes a key without revealing it.
type KeyInfo struct {
	ID     string `json:"id"`     // stable short ref derived from the key
	Name   string `json:"name"`   // what the user called it, may be empty
	Masked string `json:"masked"` // Mask of the key
	Active bool   `json:"active"` // it is the front key, tried first
	On     bool   `json:"on"`     // it is used at all
}

// KeyEntry is a key that is on, for sending requests with.
type KeyEntry struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// KeyID is the stable short reference to a key: a hash prefix, which says
// nothing about the key itself.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// keyList is all of a provider's keys in try order, front first.
func (p Provider) keyList() []ExtraKey {
	var out []ExtraKey
	if p.Key != "" {
		out = append(out, ExtraKey{Name: p.KeyName, Key: p.Key})
	}
	return append(out, p.Keys...)
}

// withKeyList stores a try-ordered list back: the first becomes the front.
func (p Provider) withKeyList(ks []ExtraKey) Provider {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(ks) == 0 {
		return p
	}
	p.Key, p.KeyName = ks[0].Key, ks[0].Name
	if len(ks) > 1 {
		p.Keys = append([]ExtraKey(nil), ks[1:]...)
	}
	return p
}

// KeyList lists every key in try order, masked.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, k := range p.keyList() {
		out = append(out, KeyInfo{ID: KeyID(k.Key), Name: k.Name, Masked: Mask(k.Key), Active: i == 0, On: i == 0 || !k.Off})
	}
	return out
}

// KeysOn lists the keys requests may use, in try order.
func (p Provider) KeysOn() []KeyEntry {
	var out []KeyEntry
	for i, k := range p.keyList() {
		if i == 0 || !k.Off {
			out = append(out, KeyEntry{Name: k.Name, Key: k.Key})
		}
	}
	return out
}

// KeyLabel is how a key shows up in messages: its name, or its masked form.
func KeyLabel(name, key string) string {
	if name != "" {
		return name
	}
	return Mask(key)
}

// normalizeKeys trims the keys, drops empty and repeated ones, and keeps
// the front key filled while any key is left.
func normalizeKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	var rest []ExtraKey
	for _, k := range p.Keys {
		k.Key, k.Name = strings.TrimSpace(k.Key), strings.TrimSpace(k.Name)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		rest = append(rest, k)
	}
	p.Keys = rest
	if p.Key == "" && len(p.Keys) > 0 {
		// no front key: the first one on takes its place (or the first, on)
		j := 0
		for i, k := range p.Keys {
			if !k.Off {
				j = i
				break
			}
		}
		front := p.Keys[j]
		p.Key, p.KeyName = front.Key, front.Name
		p.Keys = append(append([]ExtraKey(nil), p.Keys[:j]...), p.Keys[j+1:]...)
	}
	if len(p.Keys) == 0 {
		p.Keys = nil
	}
	return p
}

// keepKeys carries a stored provider's other keys over to a new save of it
// that says nothing about them (an editor that only knows the front key).
func keepKeys(p, old Provider) Provider {
	if p.Keys != nil || p.KeyName != "" {
		return p
	}
	old = normalize(old)
	p.Keys = old.Keys
	if p.Key == old.Key {
		p.KeyName = old.KeyName
	}
	return normalizeKeys(p)
}

// editKeys loads provider id, lets change rework its try-ordered keys,
// and saves the result. Nothing is written when change fails.
func editKeys(id string, change func(p Provider, ks []ExtraKey) ([]ExtraKey, error)) error {
	found, err := Find(id)
	if err != nil {
		return err
	}
	if found.Account != nil {
		return fmt.Errorf("%s signs in with its account; it takes no API keys", found.Name)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != found.ID {
			continue
		}
		p := normalize(f.Providers[i])
		ks, err := change(p, p.keyList())
		if err != nil {
			return err
		}
		f.Providers[i] = normalizeKeys(p.withKeyList(ks))
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

func keyIndex(ks []ExtraKey, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	for i, k := range ks {
		if KeyID(k.Key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no key %q — the provider's key list shows their ids", ref)
}

// nextOn is the first key after i that is on, or -1.
func nextOn(ks []ExtraKey, i int) int {
	for j := i + 1; j < len(ks); j++ {
		if !ks[j].Off {
			return j
		}
	}
	return -1
}

// toFront moves ks[j] to the front, switched on.
func toFront(ks []ExtraKey, j int) []ExtraKey {
	k := ks[j]
	k.Off = false
	out := []ExtraKey{k}
	out = append(out, ks[:j]...)
	return append(out, ks[j+1:]...)
}

// AddKey gives provider id another API key, on and tried after the ones it
// has; a provider without a key gets it as its front key.
func AddKey(id, name, key string) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("the API key is empty")
	}
	return editKeys(id, func(p Provider, ks []ExtraKey) ([]ExtraKey, error) {
		for _, k := range ks {
			if k.Key == key {
				return nil, fmt.Errorf("%s already has the key %s", p.Name, Mask(key))
			}
		}
		return append(ks, ExtraKey{Name: name, Key: key}), nil
	})
}

// UseKey makes a key the front one, switched on; the old front key stays on
// and comes right after it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p Provider, ks []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(ks, keyRef)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			return ks, nil
		}
		return toFront(ks, i), nil
	})
}

// SetKeyOn switches a key on or off. Switching the front key off hands the
// front to the next key that is on; the last key on can't be switched off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p Provider, ks []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(ks, keyRef)
		if err != nil {
			return nil, err
		}
		if on {
			ks[i].Off = false
			return ks, nil
		}
		if i != 0 {
			ks[i].Off = true
			return ks, nil
		}
		j := nextOn(ks, 0)
		if j < 0 {
			return nil, fmt.Errorf("%s is the only key of %s that is on; switch another on first", KeyLabel(ks[0].Name, ks[0].Key), p.Name)
		}
		ks[0].Off = true
		return toFront(ks, j), nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the
// next key that is on; the last key on can't be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p Provider, ks []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(ks, keyRef)
		if err != nil {
			return nil, err
		}
		if i != 0 {
			return append(ks[:i:i], ks[i+1:]...), nil
		}
		j := nextOn(ks, 0)
		if j < 0 {
			return nil, fmt.Errorf("%s is the only key of %s that is on; switch another on first", KeyLabel(ks[0].Name, ks[0].Key), p.Name)
		}
		// drop the front, then the next key on takes its place
		return toFront(ks[1:], j-1), nil
	})
}

// RenameKey names a key; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p Provider, ks []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(ks, keyRef)
		if err != nil {
			return nil, err
		}
		ks[i].Name = name
		return ks, nil
	})
}
