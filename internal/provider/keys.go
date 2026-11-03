package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// A provider can hold several API keys (accounts) of the same vendor. They
// sit in attempt order: the front key in Provider.Key / Provider.KeyName, the
// rest in Provider.Keys. The front key is always on; a key turned off stays
// in the list with Off set, so it can be switched back on later.
//
// Every change works on one ordered list of KeySlot: the list is read out of
// the provider, edited, and written back. A provider saved with a single key
// and no Keys keeps its old JSON shape.

// KeySlot is one API key of a provider, with the name the user gave it.
type KeySlot struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one of a provider's keys without revealing it.
type KeyInfo struct {
	ID     string // stable short reference, derived from the key; used by the functions below
	Name   string
	Masked string
	Active bool // first in attempt order
	On     bool // the gateway may use it
}

// keyID is the stable reference to a key: a short prefix of its SHA-256,
// which identifies the key without revealing it.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// slots lists the provider's keys in attempt order, front key first.
func (p Provider) slots() []KeySlot {
	var out []KeySlot
	if p.Key != "" {
		out = append(out, KeySlot{Name: p.KeyName, Key: p.Key})
	}
	return append(out, p.Keys...)
}

// setSlots writes an ordered key list back into the provider. An empty list
// clears the keys.
func (p *Provider) setSlots(list []KeySlot) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(list) == 0 {
		return
	}
	p.Key, p.KeyName = list[0].Key, list[0].Name
	if len(list) > 1 {
		p.Keys = append([]KeySlot(nil), list[1:]...)
	}
}

// cleanSlots trims names and keys, drops empty and repeated keys, and makes
// sure the front key is on. A file with no usable front key gets the first
// key that is on.
func cleanSlots(in []KeySlot) []KeySlot {
	var out []KeySlot
	seen := map[string]bool{}
	for _, s := range in {
		s.Key = strings.TrimSpace(s.Key)
		s.Name = strings.TrimSpace(s.Name)
		if s.Key == "" || seen[s.Key] {
			continue
		}
		seen[s.Key] = true
		out = append(out, s)
	}
	if len(out) > 0 && out[0].Off {
		j := firstOn(out, 0)
		if j < 0 {
			j = 0 // nothing is on: the first key takes the front and is switched on
		}
		out = moveToFront(out, j)
	}
	return out
}

// keySlots is the provider's cleaned, ordered key list.
func keySlots(p Provider) []KeySlot {
	return cleanSlots(p.slots())
}

// firstOn is the index of the first key at or after from that is on, or -1.
func firstOn(list []KeySlot, from int) int {
	for k := from; k < len(list); k++ {
		if !list[k].Off {
			return k
		}
	}
	return -1
}

// enabledCount counts the keys that are on.
func enabledCount(list []KeySlot) int {
	n := 0
	for _, s := range list {
		if !s.Off {
			n++
		}
	}
	return n
}

// indexOf finds a key by its reference, or -1.
func indexOf(list []KeySlot, ref string) int {
	for i, s := range list {
		if keyID(s.Key) == ref {
			return i
		}
	}
	return -1
}

// moveToFront returns a copy of list with entry j first and switched on; the
// entries before j follow it, the rest keep their order.
func moveToFront(list []KeySlot, j int) []KeySlot {
	s := list[j]
	s.Off = false
	out := make([]KeySlot, 0, len(list))
	out = append(out, s)
	out = append(out, list[:j]...)
	return append(out, list[j+1:]...)
}

// storedIndex finds a provider's raw entry in the file, or -1.
func storedIndex(f file, id string) int {
	for i := range f.Providers {
		if strings.ToLower(strings.TrimSpace(f.Providers[i].ID)) == id {
			return i
		}
	}
	return -1
}

// editKeys reads a stored provider's keys, applies change, and saves the file.
// When change fails nothing is written.
func editKeys(id string, change func(list []KeySlot) ([]KeySlot, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	i := storedIndex(f, id)
	if i < 0 {
		return fmt.Errorf("no provider %q", id)
	}
	p := &f.Providers[i]
	next, err := change(keySlots(*p))
	if err != nil {
		return err
	}
	p.setSlots(next)
	return store(f)
}

func noKey(id, ref string) error {
	return fmt.Errorf("%s has no key %q; magpie providers lists its keys", id, ref)
}

// AddKey adds a key to a provider. It is on, and comes after the keys already
// there; when the provider had no key, it becomes the front one.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("an API key is needed")
	}
	if _, ok := find(Accounts(), strings.ToLower(strings.TrimSpace(id))); ok {
		return fmt.Errorf("%s signs in with an account, so it takes no API keys", id)
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []KeySlot) ([]KeySlot, error) {
		for _, s := range list {
			if s.Key == key {
				return nil, fmt.Errorf("%s already has the key %s", id, Mask(key))
			}
		}
		return append(list, KeySlot{Name: name, Key: key}), nil
	})
}

// UseKey makes a key the front one and on. The key that was in front stays on
// and comes right after it.
func UseKey(id, ref string) error {
	ref = strings.TrimSpace(ref)
	return editKeys(id, func(list []KeySlot) ([]KeySlot, error) {
		i := indexOf(list, ref)
		if i < 0 {
			return nil, noKey(id, ref)
		}
		if i == 0 {
			return list, nil
		}
		s := list[i]
		s.Off = false
		out := []KeySlot{s, list[0]}
		for k := 1; k < len(list); k++ {
			if k != i {
				out = append(out, list[k])
			}
		}
		return out, nil
	})
}

// SetKeyOn switches a key on or off. Turning off the front key hands the front
// to the next key that is on; the last key that is on cannot be turned off.
func SetKeyOn(id, ref string, on bool) error {
	ref = strings.TrimSpace(ref)
	return editKeys(id, func(list []KeySlot) ([]KeySlot, error) {
		i := indexOf(list, ref)
		if i < 0 {
			return nil, noKey(id, ref)
		}
		if on {
			list[i].Off = false
			return list, nil
		}
		if list[i].Off {
			return list, nil
		}
		if i > 0 {
			list[i].Off = true
			return list, nil
		}
		j := firstOn(list, 1)
		if j < 0 {
			return nil, fmt.Errorf("%s needs at least one key on; turn another key on first", id)
		}
		out := moveToFront(list, j)
		out[1].Off = true // the old front key, now first after the new one
		return out, nil
	})
}

// RemoveKey deletes a key. Removing the front key hands the front to the next
// key that is on; the last key that is on cannot be removed.
func RemoveKey(id, ref string) error {
	ref = strings.TrimSpace(ref)
	return editKeys(id, func(list []KeySlot) ([]KeySlot, error) {
		i := indexOf(list, ref)
		if i < 0 {
			return nil, noKey(id, ref)
		}
		if !list[i].Off && enabledCount(list) == 1 {
			return nil, fmt.Errorf("%s needs at least one key on; turn another key on before removing this one", id)
		}
		if i > 0 {
			out := make([]KeySlot, 0, len(list)-1)
			for k, s := range list {
				if k != i {
					out = append(out, s)
				}
			}
			return out, nil
		}
		j := firstOn(list, 1)
		out := []KeySlot{list[j]}
		out[0].Off = false
		for k := 1; k < len(list); k++ {
			if k != j {
				out = append(out, list[k])
			}
		}
		return out, nil
	})
}

// RenameKey names a key. An empty name clears it.
func RenameKey(id, ref, name string) error {
	ref = strings.TrimSpace(ref)
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []KeySlot) ([]KeySlot, error) {
		i := indexOf(list, ref)
		if i < 0 {
			return nil, noKey(id, ref)
		}
		list[i].Name = name
		return list, nil
	})
}

// KeyList describes the provider's keys in attempt order, without revealing them.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, s := range keySlots(p) {
		out = append(out, KeyInfo{
			ID:     keyID(s.Key),
			Name:   s.Name,
			Masked: Mask(s.Key),
			Active: i == 0,
			On:     !s.Off,
		})
	}
	return out
}

// KeysOn lists the keys the gateway may use, in attempt order.
func (p Provider) KeysOn() []KeySlot {
	var out []KeySlot
	for _, s := range keySlots(p) {
		if !s.Off {
			out = append(out, s)
		}
	}
	return out
}
