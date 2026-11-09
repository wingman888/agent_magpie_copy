package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyEntry is one API key after the front one: the account's name (optional),
// the key itself, and Off for a key the user kept but turned off.
//
// The front key lives in Provider.Key and Provider.KeyName; the rest are in
// Provider.Keys, in the order they are tried. A provider with a single key
// and no name stores exactly what it always did.
type KeyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one key of a provider for display. ID is a stable short
// reference derived from the key; the key itself never leaves this package
// through KeyList.
type KeyInfo struct {
	ID     string
	Name   string
	Masked string
	Active bool // this is the front key, tried first
	On     bool // enabled; a disabled key is never sent to upstream
}

// keyItem is the working form of a key while an operation runs.
type keyItem struct {
	name string
	key  string
	on   bool
}

// keyRefOf is the stable reference of a key: a short hash, so the key itself
// never shows up in listings, logs or errors.
func keyRefOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// keyItems lists every key in try order: the front key first, then Keys.
// The front key is always enabled; if the first key is off it is promoted.
func (p Provider) keyItems() []keyItem {
	var out []keyItem
	if p.Key != "" {
		out = append(out, keyItem{name: p.KeyName, key: p.Key, on: true})
	}
	for _, k := range p.Keys {
		if k.Key == "" {
			continue
		}
		out = append(out, keyItem{name: k.Name, key: k.Key, on: !k.Off})
	}
	return promote(out)
}

// setKeyItems writes an ordered key list back into the provider's fields.
func (p *Provider) setKeyItems(items []keyItem) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(items) == 0 {
		return
	}
	p.Key, p.KeyName = items[0].key, items[0].name
	for _, it := range items[1:] {
		p.Keys = append(p.Keys, KeyEntry{Name: it.name, Key: it.key, Off: !it.on})
	}
}

// promote moves the first enabled key to the front, keeping the order of the
// others. The front key has to be enabled; nothing changes when none is.
func promote(items []keyItem) []keyItem {
	if len(items) == 0 || items[0].on {
		return items
	}
	for j := 1; j < len(items); j++ {
		if items[j].on {
			out := make([]keyItem, 0, len(items))
			out = append(out, items[j])
			out = append(out, items[:j]...)
			out = append(out, items[j+1:]...)
			return out
		}
	}
	return items
}

// cleanKeys trims key entries, drops empty ones and repeats of the front key
// or of each other, returning nil when nothing is left.
func cleanKeys(front string, ks []KeyEntry) []KeyEntry {
	var out []KeyEntry
	seen := map[string]bool{front: true}
	for _, k := range ks {
		k.Name, k.Key = strings.TrimSpace(k.Name), strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	return out
}

// mergeKeys keeps the other keys when a caller saves a provider without them
// (a record built from scratch rather than read back with Find). A different
// front key replaces the old one; the old front key's name goes with it.
func mergeKeys(old, p Provider) Provider {
	if p.Key == old.Key && p.KeyName == "" {
		p.KeyName = old.KeyName
	}
	if len(p.Keys) > 0 || len(old.Keys) == 0 {
		return p
	}
	p.Keys = cleanKeys(p.Key, old.Keys)
	return p
}

// findKey returns the position of the key with the given reference.
func findKey(items []keyItem, keyRef string) (int, error) {
	ref := strings.TrimSpace(keyRef)
	for i, it := range items {
		if keyRefOf(it.key) == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no key %q on this provider — KeyList shows them", keyRef)
}

// mutateKeys loads a key-based provider, applies fn to its keys and saves the
// result. When fn fails nothing is written.
func mutateKeys(id string, fn func([]keyItem) ([]keyItem, error)) error {
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it takes no API keys", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break // only model picks, not a provider
		}
		// Normalize the raw config the same way KeyList and All do.
		p.Key, p.KeyName = strings.TrimSpace(p.Key), strings.TrimSpace(p.KeyName)
		p.Keys = cleanKeys(p.Key, p.Keys)
		items, err := fn(p.keyItems())
		if err != nil {
			return err
		}
		if countOn(items) == 0 {
			return errors.New("a provider needs at least one enabled key")
		}
		p.setKeyItems(items)
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
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

// AddKey adds a key to a provider. It is enabled and tried after the keys
// already there; with no key yet it becomes the front key.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("an API key can't be empty")
	}
	return mutateKeys(id, func(items []keyItem) ([]keyItem, error) {
		for _, it := range items {
			if it.key == key {
				return nil, fmt.Errorf("provider %s already has key %s", id, Mask(key))
			}
		}
		return append(items, keyItem{name: name, key: key, on: true}), nil
	})
}

// UseKey makes a key the front one and enables it. The former front key stays
// enabled and goes right behind it.
func UseKey(id, keyRef string) error {
	return mutateKeys(id, func(items []keyItem) ([]keyItem, error) {
		i, err := findKey(items, keyRef)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			return items, nil
		}
		front := items[i]
		front.on = true
		out := make([]keyItem, 0, len(items))
		out = append(out, front)
		out = append(out, items[0])
		out = append(out, items[1:i]...)
		out = append(out, items[i+1:]...)
		out[1].on = true
		return out, nil
	})
}

// SetKeyOn enables or disables a key. Turning off the front key moves the
// next enabled key to the front. A provider always keeps one enabled key.
func SetKeyOn(id, keyRef string, on bool) error {
	return mutateKeys(id, func(items []keyItem) ([]keyItem, error) {
		i, err := findKey(items, keyRef)
		if err != nil {
			return nil, err
		}
		if items[i].on == on {
			return items, nil
		}
		items[i].on = on
		if countOn(items) == 0 {
			return nil, fmt.Errorf("%s is the only enabled key; enable another key before turning it off", Mask(items[i].key))
		}
		return promote(items), nil
	})
}

// RemoveKey deletes a key. Removing the front key moves the next enabled key
// to the front; the last enabled key can't be removed.
func RemoveKey(id, keyRef string) error {
	return mutateKeys(id, func(items []keyItem) ([]keyItem, error) {
		i, err := findKey(items, keyRef)
		if err != nil {
			return nil, err
		}
		out := make([]keyItem, 0, len(items)-1)
		out = append(out, items[:i]...)
		out = append(out, items[i+1:]...)
		if countOn(out) == 0 {
			return nil, fmt.Errorf("%s is the only enabled key; a provider needs one", Mask(items[i].key))
		}
		return promote(out), nil
	})
}

// RenameKey sets the name of a key. Trimmed; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return mutateKeys(id, func(items []keyItem) ([]keyItem, error) {
		i, err := findKey(items, keyRef)
		if err != nil {
			return nil, err
		}
		items[i].name = name
		return items, nil
	})
}

// KeyList lists the provider's keys in try order, front key first.
func (p Provider) KeyList() []KeyInfo {
	items := p.keyItems()
	out := make([]KeyInfo, 0, len(items))
	for i, it := range items {
		out = append(out, KeyInfo{
			ID:     keyRefOf(it.key),
			Name:   it.name,
			Masked: Mask(it.key),
			Active: i == 0,
			On:     it.on,
		})
	}
	return out
}

// KeysOn lists the enabled keys in try order, with names and full keys, for
// the gateway to send requests with.
func (p Provider) KeysOn() []KeyEntry {
	var out []KeyEntry
	for _, it := range p.keyItems() {
		if it.on {
			out = append(out, KeyEntry{Name: it.name, Key: it.key})
		}
	}
	return out
}
