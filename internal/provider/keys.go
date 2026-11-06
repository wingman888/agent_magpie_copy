package provider

// Several keys (accounts) per provider. The first key is the provider's
// Key field, as it always was; the others sit in Keys, in the order they are
// tried. A key that just failed rests for a minute at the back of the line,
// as a fallback provider does, and an off key is kept but never used.
//
// A provider always has its first key enabled while it has any key at all.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// KeyInfo describes one key of a provider. It never carries the full key.
type KeyInfo struct {
	ID     string // stable short id from the key; what the key functions take
	Name   string
	Masked string
	Active bool // first in line: the key requests try first
	On     bool // enabled: requests may use it
}

// KeyEntry is an enabled key, in the order requests try them.
type KeyEntry struct {
	Name string
	Key  string
}

// keyEntry is one key while it is being edited.
type keyEntry struct {
	name string
	key  string
	on   bool
}

// keyCooldown is how long a key that failed waits at the back of the line.
const keyCooldown = time.Minute

// KeyRef is the stable short id of a key: the first 8 hex digits of its
// SHA-256. It names a key without showing it.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}

// chain lists the provider's keys in the order requests try them, the first
// one always enabled.
func chain(p *Provider) []keyEntry {
	var out []keyEntry
	if p.Key != "" {
		out = append(out, keyEntry{name: p.KeyName, key: p.Key, on: true})
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			out = append(out, keyEntry{name: k.Name, key: k.Key, on: !k.Off})
		}
	}
	return frontOn(out)
}

// setChain writes the keys back into the provider's fields.
func setChain(p *Provider, ks []keyEntry) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(ks) == 0 {
		return
	}
	p.Key, p.KeyName = ks[0].key, ks[0].name
	for _, k := range ks[1:] {
		p.Keys = append(p.Keys, ExtraKey{Name: k.name, Key: k.key, Off: !k.on})
	}
}

// frontOn makes sure the first key is enabled, moving the first enabled one
// up if it is not.
func frontOn(ks []keyEntry) []keyEntry {
	if len(ks) == 0 || ks[0].on {
		return ks
	}
	if i := firstOn(ks); i > 0 {
		return promote(ks, i)
	}
	ks[0].on = true
	return ks
}

// promote moves ks[i] to the front; the keys before it keep their order
// behind it, then the rest.
func promote(ks []keyEntry, i int) []keyEntry {
	out := make([]keyEntry, 0, len(ks))
	out = append(out, ks[i])
	out = append(out, ks[:i]...)
	return append(out, ks[i+1:]...)
}

// firstOn is the index of the first enabled key, or -1.
func firstOn(ks []keyEntry) int {
	for i, k := range ks {
		if k.on {
			return i
		}
	}
	return -1
}

func countOn(ks []keyEntry) int {
	n := 0
	for _, k := range ks {
		if k.on {
			n++
		}
	}
	return n
}

func findKey(ks []keyEntry, ref string) int {
	for i, k := range ks {
		if KeyRef(k.key) == ref {
			return i
		}
	}
	return -1
}

func noKey(id, ref string) error {
	return fmt.Errorf("%s has no key %q", id, ref)
}

func lastOn(id string) error {
	return fmt.Errorf("%s needs at least one enabled key", id)
}

// editKeys applies an edit to a provider's keys and saves the result. Signed-in
// accounts have no keys to manage.
func editKeys(id string, edit func(ks []keyEntry) ([]keyEntry, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account and uses no API key", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		ks, err := edit(chain(&p))
		if err != nil {
			return err
		}
		setChain(&p, ks)
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey gives a provider another key. It is enabled and goes behind the
// keys it has; the first key a provider gets is the one tried first.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(ks []keyEntry) ([]keyEntry, error) {
		for _, k := range ks {
			if k.key == key {
				return nil, fmt.Errorf("%s already has the key %s", id, Mask(key))
			}
		}
		return append(ks, keyEntry{name: name, key: key, on: true}), nil
	})
}

// UseKey makes a key the first one tried, enabled. The key that was first
// stays enabled, right behind it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(ks []keyEntry) ([]keyEntry, error) {
		i := findKey(ks, keyRef)
		if i < 0 {
			return nil, noKey(id, keyRef)
		}
		if i == 0 {
			return ks, nil
		}
		ks[i].on = true
		return promote(ks, i), nil
	})
}

// SetKeyOn enables or disables a key. Disabling the first key hands the front
// to the next enabled one; a provider always keeps one enabled key.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(ks []keyEntry) ([]keyEntry, error) {
		i := findKey(ks, keyRef)
		if i < 0 {
			return nil, noKey(id, keyRef)
		}
		if on {
			ks[i].on = true
			return ks, nil
		}
		if !ks[i].on {
			return ks, nil
		}
		if countOn(ks) == 1 {
			return nil, lastOn(id)
		}
		ks[i].on = false
		if i == 0 {
			ks = promote(ks, firstOn(ks))
		}
		return ks, nil
	})
}

// RemoveKey deletes a key. Removing the first key hands the front to the next
// enabled one; the last enabled key cannot be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(ks []keyEntry) ([]keyEntry, error) {
		i := findKey(ks, keyRef)
		if i < 0 {
			return nil, noKey(id, keyRef)
		}
		if ks[i].on && countOn(ks) == 1 {
			return nil, lastOn(id)
		}
		if i == 0 {
			rest := ks[1:]
			return promote(rest, firstOn(rest)), nil
		}
		out := make([]keyEntry, 0, len(ks)-1)
		out = append(out, ks[:i]...)
		return append(out, ks[i+1:]...), nil
	})
}

// RenameKey names a key; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(ks []keyEntry) ([]keyEntry, error) {
		i := findKey(ks, keyRef)
		if i < 0 {
			return nil, noKey(id, keyRef)
		}
		ks[i].name = name
		return ks, nil
	})
}

// KeyList describes the provider's keys in the order they are tried.
func (p *Provider) KeyList() []KeyInfo {
	ks := chain(p)
	out := make([]KeyInfo, 0, len(ks))
	for i, k := range ks {
		out = append(out, KeyInfo{
			ID:     KeyRef(k.key),
			Name:   k.name,
			Masked: Mask(k.key),
			Active: i == 0,
			On:     k.on,
		})
	}
	return out
}

// KeysOn lists the enabled keys in the order requests should try them. A key
// that failed within the last minute is listed last, not dropped.
func (p *Provider) KeysOn() []KeyEntry {
	var ready, resting []KeyEntry
	for _, k := range chain(p) {
		if !k.on {
			continue
		}
		e := KeyEntry{Name: k.name, Key: k.key}
		if KeyResting(p.ID, KeyRef(k.key)) {
			resting = append(resting, e)
		} else {
			ready = append(ready, e)
		}
	}
	return append(ready, resting...)
}

// keyRest remembers when each key last failed, by provider and key ref.
var keyRest = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// MarkKeyFailed records that a key just failed: it rests for a minute and
// moves to the back of the line.
func MarkKeyFailed(providerID, keyRef string) {
	keyRest.Lock()
	defer keyRest.Unlock()
	keyRest.m[providerID+"/"+keyRef] = time.Now().Add(keyCooldown)
}

// KeyResting reports whether a key failed within the last minute.
func KeyResting(providerID, keyRef string) bool {
	keyRest.Lock()
	defer keyRest.Unlock()
	return time.Now().Before(keyRest.m[providerID+"/"+keyRef])
}

// keepKeys is what Save stores for a provider being replaced by p. A caller
// that does not manage the extra keys (a form, say) keeps them: the front
// key is replaced by p.Key, and the others stay unless p.Key is one of them.
// A caller that sends no key at all keeps every key as it was.
func keepKeys(old, p Provider) Provider {
	ks := chain(&old)
	if len(ks) < 2 || len(p.Keys) > 0 || p.KeyName != "" {
		return p
	}
	if p.Key == "" {
		setChain(&p, ks)
		return p
	}
	front := keyEntry{key: p.Key, on: true}
	for _, k := range ks {
		if k.key == p.Key {
			front.name = k.name
			break
		}
	}
	out := []keyEntry{front}
	for _, k := range ks[1:] {
		if k.key != p.Key {
			out = append(out, k)
		}
	}
	setChain(&p, out)
	return p
}
