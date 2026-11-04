package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// StoredKey is one API key of a provider. The front key lives in Provider.Key
// and Provider.KeyName; the rest sit in Provider.Keys, in try order. Off keeps
// a key on file without the gateway ever sending it a request. The front key
// is always on.
type StoredKey struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one key of a provider. It never carries the secret itself.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the front key: tried first
	On     bool   `json:"on"`     // enabled: the gateway may use it
}

// EnabledKey is an enabled key, in try order, with its secret.
type EnabledKey struct {
	ID   string
	Name string
	Key  string
}

// keyID is the stable, non-secret short id of a key: the first four bytes of
// its SHA-256, in hex.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// keySlice lists every key of the provider in try order, front first. Blank
// keys and repeats are dropped; a provider with no front key (only a list)
// promotes its first listed key.
func (p Provider) keySlice() []StoredKey {
	var out []StoredKey
	seen := map[string]bool{}
	add := func(name, key string, off bool) {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, StoredKey{Name: strings.TrimSpace(name), Key: key, Off: off})
	}
	add(p.KeyName, p.Key, false)
	for _, k := range p.Keys {
		add(k.Name, k.Key, k.Off)
	}
	return out
}

// setKeySlice writes an ordered key list back into the provider's fields. The
// front entry is always on.
func (p *Provider) setKeySlice(list []StoredKey) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(list) == 0 {
		return
	}
	list[0].Off = false
	p.Key, p.KeyName = list[0].Key, list[0].Name
	if len(list) > 1 {
		p.Keys = list[1:]
	}
}

// KeyList lists every key of the provider in try order.
func (p Provider) KeyList() []KeyInfo {
	list := p.keySlice()
	out := make([]KeyInfo, 0, len(list))
	for i, k := range list {
		out = append(out, KeyInfo{
			ID:     keyID(k.Key),
			Name:   k.Name,
			Masked: Mask(k.Key),
			Active: i == 0,
			On:     i == 0 || !k.Off,
		})
	}
	return out
}

// KeysOn lists the enabled keys of the provider in try order, with secrets.
func (p Provider) KeysOn() []EnabledKey {
	var out []EnabledKey
	for i, k := range p.keySlice() {
		if i == 0 || !k.Off {
			out = append(out, EnabledKey{ID: keyID(k.Key), Name: k.Name, Key: k.Key})
		}
	}
	return out
}

// AddKey adds a key to a provider. It is enabled and goes after the keys
// already there, or becomes the front key when there are none.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("an API key is required")
	}
	return mutateKeys(id, func(list []StoredKey) ([]StoredKey, error) {
		for _, k := range list {
			if k.Key == key {
				return nil, errors.New("that key is already on this provider")
			}
		}
		return append(list, StoredKey{Name: name, Key: key}), nil
	})
}

// UseKey makes the given key the front one and enables it. The key that was
// in front stays enabled, right behind it.
func UseKey(id, keyRef string) error {
	return mutateKeys(id, func(list []StoredKey) ([]StoredKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		return promote(list, i, false), nil
	})
}

// SetKeyOn enables or disables a key. Disabling the front key hands the front
// to the next enabled key; the last enabled key cannot be disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return mutateKeys(id, func(list []StoredKey) ([]StoredKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		if on {
			list[i].Off = false
			return list, nil
		}
		if i == 0 {
			j := nextOn(list)
			if j < 0 {
				return nil, errors.New("a provider must keep at least one enabled key")
			}
			return promote(list, j, true), nil
		}
		list[i].Off = true
		return list, nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the next
// enabled key; the last enabled key cannot be removed.
func RemoveKey(id, keyRef string) error {
	return mutateKeys(id, func(list []StoredKey) ([]StoredKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			j := nextOn(list)
			if j < 0 {
				return nil, errors.New("cannot remove the last enabled key; enable another key first")
			}
			out := []StoredKey{list[j]}
			out = append(out, list[1:j]...)
			out = append(out, list[j+1:]...)
			return out, nil
		}
		out := make([]StoredKey, 0, len(list)-1)
		out = append(out, list[:i]...)
		out = append(out, list[i+1:]...)
		return out, nil
	})
}

// RenameKey names a key; a blank name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return mutateKeys(id, func(list []StoredKey) ([]StoredKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		list[i].Name = name
		return list, nil
	})
}

// mutateKeys loads the provider, applies edit to its key list, and saves it.
func mutateKeys(id string, edit func([]StoredKey) ([]StoredKey, error)) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s signs in with an account, so it has no API keys to manage", p.Name)
	}
	f := load()
	for i := range f.Providers {
		q := normalize(f.Providers[i])
		if q.ID != p.ID {
			continue
		}
		list, err := edit(q.keySlice())
		if err != nil {
			return err
		}
		q.setKeySlice(list)
		f.Providers[i] = q
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// keyIndex finds the key with the given id. The error never echoes the
// reference, which could be a secret typed by mistake.
func keyIndex(list []StoredKey, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, k := range list {
		if keyID(k.Key) == ref {
			return i, nil
		}
	}
	return -1, errors.New("no such key on this provider")
}

// nextOn is the index of the first enabled key behind the front, or -1.
func nextOn(list []StoredKey) int {
	for j := 1; j < len(list); j++ {
		if !list[j].Off {
			return j
		}
	}
	return -1
}

// promote moves list[i] to the front and enables it. The old front follows it,
// disabled when demoteFront is set.
func promote(list []StoredKey, i int, demoteFront bool) []StoredKey {
	if i == 0 {
		return list
	}
	top := list[i]
	top.Off = false
	old := list[0]
	if demoteFront {
		old.Off = true
	}
	out := make([]StoredKey, 0, len(list))
	out = append(out, top, old)
	out = append(out, list[1:i]...)
	out = append(out, list[i+1:]...)
	return out
}
