package provider

// Several keys (accounts) on one vendor.
//
// A provider keeps its keys as one ordered list, front first: the front key
// is stored in Key/KeyName, the others in Keys. Attempts go in list order.
// The front key is always on; a key the user turns off stays in the list
// with off set, so it can be turned back on later without losing its place.
// Every provider keeps at least one key on.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// keyEntry is one key in a provider's list, as stored in providers.json.
type keyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// NamedKey is a key that can be used for a request, with its name.
type NamedKey struct {
	Name string
	Key  string
}

// KeyInfo describes one key on a provider. It never carries the key itself.
type KeyInfo struct {
	ID     string `json:"id"`     // stable short id, derived from the key
	Name   string `json:"name"`   // the user's name for it, may be empty
	Masked string `json:"masked"` // the key, hidden but for its ends
	Active bool   `json:"active"` // this is the front key, tried first
	On     bool   `json:"on"`     // off keys are never sent to the vendor
}

var (
	errLastOn    = errors.New("a provider needs at least one key on; turn another key on first")
	errKeyEmpty  = errors.New("the key is empty")
	errKeyExists = errors.New("this provider already has that key")
)

// keyID is a short id for a key that does not reveal it.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// keyList is the provider's keys in attempt order, cleaned: blank and
// repeated keys dropped, and a front key always on. A provider with no front
// key takes its first on key as the front.
func keyList(p Provider) []keyEntry {
	var raw []keyEntry
	if p.Key != "" {
		raw = append(raw, keyEntry{Name: p.KeyName, Key: p.Key})
	}
	raw = append(raw, p.Keys...)
	var out []keyEntry
	seen := map[string]bool{}
	for _, e := range raw {
		e.Name = strings.TrimSpace(e.Name)
		e.Key = strings.TrimSpace(e.Key)
		if e.Key == "" || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		out = append(out, e)
	}
	if len(out) > 0 && out[0].Off {
		k := firstOn(out)
		if k < 0 {
			k = 0
		}
		return front(out, k, true)
	}
	return out
}

// setKeys writes a key list back into the provider's fields.
func setKeys(p Provider, list []keyEntry) Provider {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(list) == 0 {
		return p
	}
	p.Key, p.KeyName = list[0].Key, list[0].Name
	if len(list) > 1 {
		p.Keys = append([]keyEntry(nil), list[1:]...)
	}
	return p
}

// firstOn is the index of the first key after the front that is on, or -1.
func firstOn(list []keyEntry) int {
	for i := 1; i < len(list); i++ {
		if !list[i].Off {
			return i
		}
	}
	return -1
}

// front moves list[k] to the front, on. The old front stays right behind
// it, off when oldOff is set.
func front(list []keyEntry, k int, oldOff bool) []keyEntry {
	chosen := list[k]
	chosen.Off = false
	old := list[0]
	old.Off = oldOff
	out := []keyEntry{chosen}
	if k != 0 {
		out = append(out, old)
	}
	for i := 1; i < len(list); i++ {
		if i != k {
			out = append(out, list[i])
		}
	}
	return out
}

// keepKeys gives a saved provider its keys back when the save did not
// mention them. A save with no key keeps the saved front key; a save with the
// same key keeps its name; a new front key replaces the old one and the
// others stay.
func keepKeys(p, old Provider) Provider {
	if p.Key == "" || p.Key == old.Key {
		if p.Key == "" {
			p.Key = old.Key
		}
		if p.KeyName == "" {
			p.KeyName = old.KeyName
		}
	}
	p.Keys = old.Keys
	return p
}

// savedProvider returns the saved provider with this id, as the file has it.
func savedProvider(id string) (Provider, bool) {
	for _, p := range load().Providers {
		if p.ID == id {
			return normalize(p), true
		}
	}
	return Provider{}, false
}

// editKeys applies edit to a saved provider's key list and writes it back.
// Signed-in accounts have no keys of their own.
func editKeys(id string, edit func(list []keyEntry) ([]keyEntry, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account, which has no keys of its own", id)
	}
	f := load()
	for i := range f.Providers {
		p := f.Providers[i]
		if p.ID != id {
			continue
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break
		}
		list, err := edit(keyList(p))
		if err != nil {
			return err
		}
		f.Providers[i] = setKeys(p, list)
		return store(f)
	}
	return fmt.Errorf("no provider %q — magpie providers lists them", id)
}

// indexOfKey finds a key in the list by its id.
func indexOfKey(list []keyEntry, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, e := range list {
		if keyID(e.Key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no key %q on this provider", ref)
}

// AddKey adds a key to a provider. It is on, and goes behind the keys already
// there; a provider with no key gets it as its front key.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errKeyEmpty
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []keyEntry) ([]keyEntry, error) {
		for _, e := range list {
			if e.Key == key {
				return nil, errKeyExists
			}
		}
		return append(list, keyEntry{Name: name, Key: key}), nil
	})
}

// UseKey makes a key the front one and turns it on. The key that was in
// front stays on, right behind it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(list []keyEntry) ([]keyEntry, error) {
		j, err := indexOfKey(list, keyRef)
		if err != nil {
			return nil, err
		}
		if j == 0 {
			return list, nil
		}
		return front(list, j, false), nil
	})
}

// SetKeyOn turns a key on or off. Turning off the front key hands the front
// to the next key that is on; a provider always keeps one key on.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(list []keyEntry) ([]keyEntry, error) {
		j, err := indexOfKey(list, keyRef)
		if err != nil {
			return nil, err
		}
		if on {
			list[j].Off = false
			return list, nil
		}
		if list[j].Off {
			return list, nil
		}
		if j > 0 {
			list[j].Off = true
			return list, nil
		}
		k := firstOn(list)
		if k < 0 {
			return nil, errLastOn
		}
		return front(list, k, true), nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the next
// key that is on; the last key on cannot be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(list []keyEntry) ([]keyEntry, error) {
		j, err := indexOfKey(list, keyRef)
		if err != nil {
			return nil, err
		}
		if j > 0 {
			return append(append([]keyEntry{}, list[:j]...), list[j+1:]...), nil
		}
		k := firstOn(list)
		if k < 0 {
			return nil, errLastOn
		}
		out := []keyEntry{list[k]}
		for i := 1; i < len(list); i++ {
			if i != k {
				out = append(out, list[i])
			}
		}
		return out, nil
	})
}

// RenameKey names a key. An empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []keyEntry) ([]keyEntry, error) {
		j, err := indexOfKey(list, keyRef)
		if err != nil {
			return nil, err
		}
		list[j].Name = name
		return list, nil
	})
}

// KeyList lists every key on the provider in attempt order, front first.
// Only masked keys are returned.
func (p Provider) KeyList() []KeyInfo {
	list := keyList(p)
	out := make([]KeyInfo, 0, len(list))
	for i, e := range list {
		out = append(out, KeyInfo{
			ID:     keyID(e.Key),
			Name:   e.Name,
			Masked: Mask(e.Key),
			Active: i == 0,
			On:     !e.Off,
		})
	}
	return out
}

// KeysOn lists the keys that may be sent, in attempt order. A provider with
// no key at all (a local server, a signed-in account) gets one attempt with
// an empty key.
func (p Provider) KeysOn() []NamedKey {
	list := keyList(p)
	if len(list) == 0 {
		return []NamedKey{{}}
	}
	var out []NamedKey
	for _, e := range list {
		if !e.Off {
			out = append(out, NamedKey{Name: e.Name, Key: e.Key})
		}
	}
	return out
}
