package provider

// Keys: a provider may hold several API keys (accounts). Key is the one
// tried first and is always enabled; Keys are the rest in order, each of
// which may be kept but switched off.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyEntry is an additional key of a provider.
type KeyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"` // kept, but never used
}

// KeyInfo describes one key without revealing it.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the first one tried
	On     bool   `json:"on"`
}

// KeyOn is an enabled key as the gateway uses it.
type KeyOn struct {
	Name string
	Key  string
}

// keyID is a stable short handle derived from the key, safe to show.
func keyID(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:8]
}

type slot struct {
	name, key string
	on        bool
}

func (p Provider) slots() []slot {
	var out []slot
	if p.Key != "" {
		out = append(out, slot{p.KeyName, p.Key, true})
	}
	for _, k := range p.Keys {
		out = append(out, slot{k.Name, k.Key, !k.Off})
	}
	return out
}

// setSlots stores an ordered list; the first must be enabled.
func (p Provider) setSlots(l []slot) Provider {
	p.Key, p.KeyName, p.Keys = "", "", nil
	for i, s := range l {
		if i == 0 {
			p.Key, p.KeyName = s.key, s.name
			continue
		}
		p.Keys = append(p.Keys, KeyEntry{Name: s.name, Key: s.key, Off: !s.on})
	}
	return p
}

// normalizeKeys trims, drops empty and duplicate keys, and makes sure the
// first key is an enabled one.
func normalizeKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	if p.Key == "" {
		p.KeyName = ""
	}
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	var rest []slot
	for _, k := range p.Keys {
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		rest = append(rest, slot{strings.TrimSpace(k.Name), k.Key, !k.Off})
	}
	l := rest
	if p.Key != "" {
		l = append([]slot{{p.KeyName, p.Key, true}}, rest...)
	} else if i := firstOn(l); i > 0 {
		l[0], l[i] = l[i], l[0]
	} else if i < 0 && len(l) > 0 {
		l[0].on = true
	}
	return p.setSlots(l)
}

func firstOn(l []slot) int {
	for i, s := range l {
		if s.on {
			return i
		}
	}
	return -1
}

// KeyList describes every key in the order they are tried.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, s := range p.slots() {
		out = append(out, KeyInfo{ID: keyID(s.key), Name: s.name, Masked: Mask(s.key), Active: i == 0, On: s.on})
	}
	return out
}

// KeysOn lists the enabled keys in the order they are tried.
func (p Provider) KeysOn() []KeyOn {
	var out []KeyOn
	for _, s := range p.slots() {
		if s.on {
			out = append(out, KeyOn{s.name, s.key})
		}
	}
	return out
}

// editKeys loads the stored provider, lets fn change its key list, and
// saves it. fn gets the list and the index of keyRef (-1 when keyRef is "").
func editKeys(id, keyRef string, fn func(l []slot, i int) ([]slot, error)) error {
	q := strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), q); ok {
		return fmt.Errorf("%s is a signed-in account and has no API keys", q)
	}
	f := load()
	for n := range f.Providers {
		sp := &f.Providers[n]
		if sp.ID != q && strings.ToLower(strings.TrimSpace(sp.Name)) != q {
			continue
		}
		cur := normalize(*sp)
		if cur.Chat == "" && cur.Responses == "" && cur.Anthropic == "" {
			return fmt.Errorf("no provider %q", id)
		}
		l := cur.slots()
		i := -1
		if keyRef != "" {
			for j, s := range l {
				if keyID(s.key) == keyRef {
					i = j
				}
			}
			if i < 0 {
				return fmt.Errorf("%s has no key %q", cur.ID, keyRef)
			}
		}
		l, err := fn(l, i)
		if err != nil {
			return err
		}
		*sp = cur.setSlots(l)
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key behind the existing ones, enabled; the first key of a
// provider becomes its front one.
func AddKey(id, name, key string) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	return editKeys(id, "", func(l []slot, _ int) ([]slot, error) {
		for _, s := range l {
			if s.key == key {
				return nil, fmt.Errorf("that key is already there (%s)", Mask(key))
			}
		}
		return append(l, slot{name, key, true}), nil
	})
}

// UseKey makes a key the front one (and enables it); the old front key
// stays enabled right behind it.
func UseKey(id, keyRef string) error {
	return editKeys(id, keyRef, func(l []slot, i int) ([]slot, error) {
		if i == 0 {
			return l, nil
		}
		s := l[i]
		s.on = true
		rest := append(append([]slot{}, l[:i]...), l[i+1:]...)
		return append([]slot{s}, rest...), nil
	})
}

// promote moves the first enabled key after the front one to the front,
// keeping the front key (when keep) right behind it. It fails when no other
// key is enabled.
func promote(l []slot, keep bool) ([]slot, error) {
	j := -1
	for k := 1; k < len(l); k++ {
		if l[k].on {
			j = k
			break
		}
	}
	if j < 0 {
		return nil, errors.New("at least one key must stay enabled")
	}
	out := []slot{l[j]}
	if keep {
		out = append(out, l[0])
	}
	out = append(out, l[1:j]...)
	return append(out, l[j+1:]...), nil
}

// SetKeyOn enables or disables a key. The last enabled key can't be disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, keyRef, func(l []slot, i int) ([]slot, error) {
		if i == 0 {
			if on {
				return l, nil
			}
			l[0].on = false
			out, err := promote(l, true)
			return out, err
		}
		l[i].on = on
		return l, nil
	})
}

// RemoveKey deletes a key. The last enabled key can't be deleted.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, keyRef, func(l []slot, i int) ([]slot, error) {
		if i == 0 {
			return promote(l, false)
		}
		return append(l[:i:i], l[i+1:]...), nil
	})
}

// RenameKey names a key; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, keyRef, func(l []slot, i int) ([]slot, error) {
		l[i].name = strings.TrimSpace(name)
		return l, nil
	})
}
