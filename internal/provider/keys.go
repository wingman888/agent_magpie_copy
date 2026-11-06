package provider

// Several API keys per provider, each one an account of the same vendor.
//
// The config keeps the front key in Key/KeyName, as it always did, and the
// rest in Keys (each with an Off flag). The effective list, in the order
// magpie tries them, is the front key followed by Keys. Invariants:
//   - the front key is always enabled, so a provider with any key always has
//     at least one enabled key;
//   - keys are unique within a provider;
//   - a provider with one key looks exactly as it did before this file existed.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyInfo describes one key of a provider without revealing it.
type KeyInfo struct {
	ID     string // stable short id derived from the key; use it as keyRef
	Name   string
	Masked string
	Active bool // this is the front key, tried first
	On     bool // enabled; off keys are never sent upstream
}

// KeyCred is an enabled key, with its name, in the order it is tried.
type KeyCred struct {
	Name string
	Key  string
}

// keySlot is a key in the effective order, with its enabled state.
type keySlot struct {
	name string
	key  string
	on   bool
}

// keyID is a short, stable reference to a key that does not reveal it.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:10]
}

// cleanKeys trims the stored keys and drops empty and duplicate ones,
// returning nil when nothing is left so the field stays out of the JSON.
func cleanKeys(front string, ks []StoredKey) []StoredKey {
	seen := map[string]bool{}
	if front != "" {
		seen[front] = true
	}
	var out []StoredKey
	for _, k := range ks {
		k.Name = strings.TrimSpace(k.Name)
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	return out
}

// slots lists the provider's keys in the order they are tried. A provider
// whose front key is empty but which has stored keys promotes the first one.
func (p Provider) slots() []keySlot {
	var out []keySlot
	rest := cleanKeys(p.Key, p.Keys)
	if p.Key != "" {
		out = append(out, keySlot{name: p.KeyName, key: p.Key, on: true})
	} else if len(rest) > 0 {
		out = append(out, keySlot{name: rest[0].Name, key: rest[0].Key, on: true})
		rest = rest[1:]
	}
	for _, k := range rest {
		out = append(out, keySlot{name: k.Name, key: k.Key, on: !k.Off})
	}
	return out
}

// writeSlots stores the keys back into the provider in the given order. The
// first slot becomes the front key and must be enabled.
func writeSlots(p *Provider, ss []keySlot) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(ss) == 0 {
		return
	}
	p.Key, p.KeyName = ss[0].key, ss[0].name
	for _, s := range ss[1:] {
		p.Keys = append(p.Keys, StoredKey{Name: s.name, Key: s.key, Off: !s.on})
	}
}

func countOn(ss []keySlot) int {
	n := 0
	for _, s := range ss {
		if s.on {
			n++
		}
	}
	return n
}

// firstOn is the index of the first enabled slot at or after from, or -1.
func firstOn(ss []keySlot, from int) int {
	for i := from; i < len(ss); i++ {
		if ss[i].on {
			return i
		}
	}
	return -1
}

func keyIndex(ss []keySlot, ref string) int {
	ref = strings.TrimSpace(ref)
	for i, s := range ss {
		if keyID(s.key) == ref {
			return i
		}
	}
	return -1
}

// KeyList returns every key of the provider in the order they are tried.
// Keys are shown masked only.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, s := range p.slots() {
		out = append(out, KeyInfo{
			ID:     keyID(s.key),
			Name:   s.name,
			Masked: Mask(s.key),
			Active: i == 0,
			On:     s.on,
		})
	}
	return out
}

// KeysOn returns the enabled keys, with their names, in the order they are tried.
func (p Provider) KeysOn() []KeyCred {
	var out []KeyCred
	for _, s := range p.slots() {
		if s.on {
			out = append(out, KeyCred{Name: s.name, Key: s.key})
		}
	}
	return out
}

// editKeys runs fn on the stored provider id and saves the config when fn
// succeeds. Signed-in accounts have no keys to manage.
func editKeys(id string, fn func(p *Provider) error) error {
	q := strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), q); ok {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", q)
	}
	f := load()
	for i := range f.Providers {
		p := &f.Providers[i]
		if p.ID != q {
			continue
		}
		p.Key = strings.TrimSpace(p.Key)
		p.KeyName = strings.TrimSpace(p.KeyName)
		p.Keys = cleanKeys(p.Key, p.Keys)
		if err := fn(p); err != nil {
			return err
		}
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to the provider. It is enabled and tried after the keys
// already there, or becomes the front key when the provider has none.
func AddKey(id, name, key string) error {
	name = strings.TrimSpace(name)
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key 不能为空")
	}
	return editKeys(id, func(p *Provider) error {
		ss := p.slots()
		for _, s := range ss {
			if s.key == key {
				return errors.New("这个 API key 已经添加过了")
			}
		}
		writeSlots(p, append(ss, keySlot{name: name, key: key, on: true}))
		return nil
	})
}

// UseKey makes the given key the front one and enabled. The former front key
// stays enabled and comes right after it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ss := p.slots()
		i := keyIndex(ss, keyRef)
		if i < 0 {
			return fmt.Errorf("no key %q for provider %s", keyRef, id)
		}
		if i == 0 {
			return nil
		}
		x := ss[i]
		x.on = true
		ns := []keySlot{x, ss[0]}
		ns = append(ns, ss[1:i]...)
		ns = append(ns, ss[i+1:]...)
		writeSlots(p, ns)
		return nil
	})
}

// SetKeyOn enables or disables a key. Disabling the front key hands the front
// to the next enabled key, and the disabled one stays on file. At least one
// key always stays enabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		ss := p.slots()
		i := keyIndex(ss, keyRef)
		if i < 0 {
			return fmt.Errorf("no key %q for provider %s", keyRef, id)
		}
		if on {
			ss[i].on = true
			writeSlots(p, ss)
			return nil
		}
		if i != 0 {
			ss[i].on = false
			writeSlots(p, ss)
			return nil
		}
		j := firstOn(ss, 1)
		if j < 0 {
			return errors.New("至少要保留一个启用的 Key,这是最后一个启用的 Key,不能停用")
		}
		front := ss[j]
		front.on = true
		old := ss[0]
		old.on = false
		ns := []keySlot{front, old}
		ns = append(ns, ss[1:j]...)
		ns = append(ns, ss[j+1:]...)
		writeSlots(p, ns)
		return nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the next
// enabled key. The last enabled key cannot be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ss := p.slots()
		i := keyIndex(ss, keyRef)
		if i < 0 {
			return fmt.Errorf("no key %q for provider %s", keyRef, id)
		}
		if ss[i].on && countOn(ss) == 1 {
			return errors.New("这是最后一个启用的 Key,不能删除")
		}
		var ns []keySlot
		if i == 0 {
			j := firstOn(ss, 1)
			ns = []keySlot{ss[j]}
			ns = append(ns, ss[1:j]...)
			ns = append(ns, ss[j+1:]...)
		} else {
			ns = append(ns, ss[:i]...)
			ns = append(ns, ss[i+1:]...)
		}
		writeSlots(p, ns)
		return nil
	})
}

// RenameKey sets the name of a key. An empty name clears it.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ss := p.slots()
		i := keyIndex(ss, keyRef)
		if i < 0 {
			return fmt.Errorf("no key %q for provider %s", keyRef, id)
		}
		ss[i].name = name
		writeSlots(p, ss)
		return nil
	})
}
