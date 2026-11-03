package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// A provider can hold several API keys (accounts) at once. The front key is
// Provider.Key (named by Provider.KeyName); Provider.Keys holds the rest in the
// order magpie tries them. A key with Off set is kept but never used.
//
// Keys are addressed by keyID, a short hash of the key itself, so a listing
// can refer to one without showing it.

// KeyEntry is a key beyond the front one, as stored in providers.json.
type KeyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"` // kept, but not used
}

// KeyInfo describes one key for listings. It never carries the key itself.
type KeyInfo struct {
	ID     string // stable reference, see keyID
	Name   string
	Masked string
	Active bool // first in line: requests go to it first
	On     bool // enabled; a disabled key is never sent a request
}

// Credential is an enabled key, in the order requests try them.
type Credential struct {
	ID   string
	Name string
	Key  string
}

// Label names the account for logs and call records: "id(name)", or
// "id(masked key)" when the key has no name.
func (c Credential) Label(providerID string) string {
	if c.Name != "" {
		return providerID + "(" + c.Name + ")"
	}
	return providerID + "(" + Mask(c.Key) + ")"
}

// keyItem is one key in the working list; the front key is items[0].
type keyItem struct {
	name string
	key  string
	on   bool
}

// keyID is the stable reference for a key: a hash prefix, never the key.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// cleanKeys drops blank and repeated keys (including the front one) from the
// extra keys, trimming their fields.
func cleanKeys(front string, keys []KeyEntry) []KeyEntry {
	var out []KeyEntry
	seen := map[string]bool{front: true}
	for _, k := range keys {
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

// keyItems is the provider's keys in try order. The front key is always on.
func keyItems(p *Provider) []keyItem {
	var items []keyItem
	if p.Key != "" {
		items = append(items, keyItem{name: p.KeyName, key: p.Key, on: true})
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			items = append(items, keyItem{name: k.Name, key: k.Key, on: !k.Off})
		}
	}
	return items
}

// setKeyItems writes a working list back into the stored fields.
func setKeyItems(p *Provider, items []keyItem) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(items) == 0 {
		return
	}
	p.Key, p.KeyName = items[0].key, items[0].name
	for _, it := range items[1:] {
		p.Keys = append(p.Keys, KeyEntry{Name: it.name, Key: it.key, Off: !it.on})
	}
}

func countOn(items []keyItem) int {
	n := 0
	for _, it := range items {
		if it.on {
			n++
		}
	}
	return n
}

// firstOn is the index of the first enabled key, or -1.
func firstOn(items []keyItem) int {
	for i, it := range items {
		if it.on {
			return i
		}
	}
	return -1
}

// moveToFront returns items with items[j] first and the rest in order.
func moveToFront(items []keyItem, j int) []keyItem {
	out := make([]keyItem, 0, len(items))
	out = append(out, items[j])
	for i, it := range items {
		if i != j {
			out = append(out, it)
		}
	}
	return out
}

func findKey(p *Provider, items []keyItem, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	for i := range items {
		if keyID(items[i].key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%s has no key %q (see the key list for ids)", p.Name, ref)
}

// editKeys loads the provider, lets edit change its key list, and saves the
// result. Only the key fields of the stored entry are touched, so presets'
// defaults are not written back into the file.
func editKeys(id string, edit func(p *Provider, items []keyItem) ([]keyItem, error)) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", p.Name)
	}
	items, err := edit(p, keyItems(p))
	if err != nil {
		return err
	}
	if len(items) > 0 && countOn(items) == 0 {
		return fmt.Errorf("%s needs at least one enabled key", p.Name)
	}
	f := load()
	for i := range f.Providers {
		if normalize(f.Providers[i]).ID == p.ID {
			setKeyItems(&f.Providers[i], items)
			return store(f)
		}
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. It is enabled and goes after the existing
// keys; if the provider had none, it becomes the front key.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("the API key is empty")
	}
	return editKeys(id, func(p *Provider, items []keyItem) ([]keyItem, error) {
		for _, it := range items {
			if it.key == key {
				return nil, fmt.Errorf("%s already has that key (%s)", p.Name, Mask(key))
			}
		}
		return append(items, keyItem{name: name, key: key, on: true}), nil
	})
}

// UseKey makes a key the front one and enabled. The key that was in front
// stays enabled and comes right after it.
func UseKey(id, ref string) error {
	return editKeys(id, func(p *Provider, items []keyItem) ([]keyItem, error) {
		i, err := findKey(p, items, ref)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			return items, nil
		}
		items[i].on = true
		return moveToFront(items, i), nil
	})
}

// SetKeyOn enables or disables a key. Disabling the front key hands the front
// to the next enabled key; the disabled one stays in the list. It is an error
// to leave the provider without an enabled key.
func SetKeyOn(id, ref string, on bool) error {
	return editKeys(id, func(p *Provider, items []keyItem) ([]keyItem, error) {
		i, err := findKey(p, items, ref)
		if err != nil {
			return nil, err
		}
		if items[i].on == on {
			return items, nil
		}
		if on {
			items[i].on = true
			return items, nil
		}
		if countOn(items) <= 1 {
			return nil, fmt.Errorf("%s needs at least one enabled key; enable another key first", p.Name)
		}
		items[i].on = false
		if i == 0 {
			items = moveToFront(items, firstOn(items))
		}
		return items, nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the next
// enabled key. The last enabled key cannot be removed.
func RemoveKey(id, ref string) error {
	return editKeys(id, func(p *Provider, items []keyItem) ([]keyItem, error) {
		i, err := findKey(p, items, ref)
		if err != nil {
			return nil, err
		}
		if items[i].on && countOn(items) <= 1 {
			return nil, fmt.Errorf("%s needs at least one enabled key; this is its last one", p.Name)
		}
		out := make([]keyItem, 0, len(items)-1)
		for j, it := range items {
			if j != i {
				out = append(out, it)
			}
		}
		if i == 0 {
			if j := firstOn(out); j >= 0 {
				out = moveToFront(out, j)
			}
		}
		return out, nil
	})
}

// RenameKey names a key. An empty name clears it.
func RenameKey(id, ref, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider, items []keyItem) ([]keyItem, error) {
		i, err := findKey(p, items, ref)
		if err != nil {
			return nil, err
		}
		items[i].name = name
		return items, nil
	})
}

// KeyList describes every key in try order, front first. Keys are shown
// masked.
func (p *Provider) KeyList() []KeyInfo {
	items := keyItems(p)
	out := make([]KeyInfo, 0, len(items))
	for i, it := range items {
		out = append(out, KeyInfo{
			ID:     keyID(it.key),
			Name:   it.name,
			Masked: Mask(it.key),
			Active: i == 0,
			On:     it.on,
		})
	}
	return out
}

// KeysOn lists the enabled keys in try order. A provider with no keys at all
// (a local server, or a signed-in account) returns one empty Credential, so a
// request is still tried once, unkeyed.
func (p *Provider) KeysOn() []Credential {
	items := keyItems(p)
	if len(items) == 0 {
		return []Credential{{}}
	}
	var out []Credential
	for _, it := range items {
		if it.on {
			out = append(out, Credential{ID: keyID(it.key), Name: it.name, Key: it.key})
		}
	}
	return out
}
