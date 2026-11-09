package provider

// Several API keys per provider, for one vendor's several accounts.
//
// The front key lives in the provider's `key` (and `keyName`), exactly as
// before, so everything that reads `key` keeps working. The others live in
// `keys`, each with an optional name and an `off` flag. A key is never
// stored twice, and a provider with a single key stores exactly what it
// always did.
//
// Order is attempt order: the enabled keys first, the front one leading,
// then the disabled ones. Disabled keys are kept but never tried.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is a key after the front one, as stored in providers.json.
type ExtraKey struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one key for listing. It never carries the full key.
type KeyInfo struct {
	ID     string // stable short id derived from the key; what keyRef means
	Name   string
	Masked string
	Active bool // the front key, tried first
	On     bool // enabled: may be tried
}

// ActiveKey is an enabled key, in the order it is tried.
type ActiveKey struct {
	Name string
	Key  string
}

type keySlot struct {
	name, key string
	on        bool
}

// keyID is a short, stable id for a key: the first four bytes of its
// SHA-256, in hex. It identifies the key without revealing it.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// slots lists the provider's keys: the front one first (always enabled),
// then the others as stored. Empty and repeated keys are skipped.
func slots(p Provider) []keySlot {
	var out []keySlot
	seen := map[string]bool{}
	add := func(name, key string, on bool) {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, keySlot{name: strings.TrimSpace(name), key: key, on: on})
	}
	add(p.KeyName, p.Key, true)
	for _, k := range p.Keys {
		add(k.Name, k.Key, !k.Off)
	}
	return out
}

// ordered puts the enabled keys first, keeping their order, then the disabled.
func ordered(ss []keySlot) []keySlot {
	var on, off []keySlot
	for _, s := range ss {
		if s.on {
			on = append(on, s)
		} else {
			off = append(off, s)
		}
	}
	return append(on, off...)
}

func enabledCount(ss []keySlot) int {
	n := 0
	for _, s := range ss {
		if s.on {
			n++
		}
	}
	return n
}

// writeKeys stores ss back into p: the first key becomes `key`/`keyName`,
// the rest become `keys`. It refuses a list with no enabled key at the front.
func writeKeys(p *Provider, ss []keySlot) error {
	ss = ordered(ss)
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(ss) == 0 {
		return nil
	}
	if !ss[0].on {
		return errors.New("a provider needs at least one enabled key")
	}
	p.Key, p.KeyName = ss[0].key, ss[0].name
	for _, s := range ss[1:] {
		p.Keys = append(p.Keys, ExtraKey{Name: s.name, Key: s.key, Off: !s.on})
	}
	return nil
}

// findKey returns the index of the key with the given id, or -1.
func findKey(ss []keySlot, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1
	}
	for i, s := range ss {
		if keyID(s.key) == ref {
			return i
		}
	}
	return -1
}

func errNoKey(id string) error {
	// The ref is not echoed back: it may have been pasted as a full key.
	return fmt.Errorf("no such key on %s", id)
}

// editKeys runs change on the stored provider id and saves the result only
// when change succeeds. Signed-in accounts have no keys of their own.
func editKeys(id string, change func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", id)
	}
	f := load()
	for i := range f.Providers {
		p := &f.Providers[i]
		if p.ID != id {
			continue
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break // only model picks, not a provider
		}
		if err := change(p); err != nil {
			return err
		}
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to provider id under the given name. It is enabled and
// goes after the existing keys; with no key yet, it becomes the front one.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key is empty")
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ss := slots(*p)
		for _, s := range ss {
			if s.key == key {
				return fmt.Errorf("that key is already on %s (%s)", p.ID, Mask(key))
			}
		}
		return writeKeys(p, append(ss, keySlot{name: name, key: key, on: true}))
	})
}

// UseKey makes the key keyRef the front one and enabled. The key that was
// in front stays enabled and comes right after it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ss := slots(*p)
		i := findKey(ss, keyRef)
		if i < 0 {
			return errNoKey(p.ID)
		}
		if i == 0 {
			return nil // already the front key, and the front is always enabled
		}
		s := ss[i]
		s.on = true
		rest := append(append([]keySlot{}, ss[:i]...), ss[i+1:]...)
		return writeKeys(p, append([]keySlot{s}, rest...))
	})
}

// SetKeyOn enables or disables the key keyRef. Turning off the front key
// lets the next enabled key take its place. The last enabled key cannot
// be turned off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		ss := slots(*p)
		i := findKey(ss, keyRef)
		if i < 0 {
			return errNoKey(p.ID)
		}
		if ss[i].on == on {
			return nil
		}
		ss[i].on = on
		if enabledCount(ss) == 0 {
			return errors.New("a provider needs at least one enabled key; enable another key first")
		}
		return writeKeys(p, ss)
	})
}

// RemoveKey deletes the key keyRef. Removing the front key lets the next
// enabled key take its place. The last enabled key cannot be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ss := slots(*p)
		i := findKey(ss, keyRef)
		if i < 0 {
			return errNoKey(p.ID)
		}
		rest := append(append([]keySlot{}, ss[:i]...), ss[i+1:]...)
		if enabledCount(rest) == 0 {
			return errors.New("cannot remove the last enabled key; add or enable another key first")
		}
		return writeKeys(p, rest)
	})
}

// RenameKey names the key keyRef. An empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ss := slots(*p)
		i := findKey(ss, keyRef)
		if i < 0 {
			return errNoKey(p.ID)
		}
		ss[i].name = name
		return writeKeys(p, ss)
	})
}

// KeyList describes every key of the provider in attempt order.
func (p Provider) KeyList() []KeyInfo {
	ss := ordered(slots(p))
	out := make([]KeyInfo, 0, len(ss))
	for i, s := range ss {
		out = append(out, KeyInfo{ID: keyID(s.key), Name: s.name, Masked: Mask(s.key), Active: i == 0, On: s.on})
	}
	return out
}

// KeysOn lists the enabled keys in the order they are tried.
func (p Provider) KeysOn() []ActiveKey {
	var out []ActiveKey
	for _, s := range ordered(slots(p)) {
		if s.on {
			out = append(out, ActiveKey{Name: s.name, Key: s.key})
		}
	}
	return out
}
