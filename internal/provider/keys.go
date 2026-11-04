package provider

// Several API keys ("accounts") per provider.
//
// A provider keeps its keys as one ordered list, the order they are tried in.
// The first one is the provider's key, stored in key/keyName exactly as it
// always was; the rest sit in keys. A key the user switched off stays in the
// list with off set. The first key is always an enabled one, and at least one
// key is always enabled.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// KeyEntry is a stored key beyond the first.
type KeyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one key of a provider for listings. It never carries the
// key itself: only its stable id and its mask.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // first in the order: tried first
	On     bool   `json:"on"`     // enabled: may be tried at all
}

// Credential is an enabled key, ready to be tried.
type Credential struct {
	ID   string
	Name string
	Key  string
}

// keyRest is how long a key that just failed is left to the back of the line.
const keyRest = time.Minute

// keyRests remembers, per provider and key, until when a key is resting.
var keyRests = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

// refOf is the stable short id of a key: a digest of its content, so a key
// never has to be shown to refer to it.
func refOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// ring is the provider's keys in the order they are tried, cleaned up: no
// blanks, no duplicates, and an enabled first key.
func (p Provider) ring() []KeyEntry {
	var out []KeyEntry
	seen := map[string]bool{}
	add := func(e KeyEntry) {
		e.Key = strings.TrimSpace(e.Key)
		e.Name = strings.TrimSpace(e.Name)
		if e.Key == "" || seen[e.Key] {
			return
		}
		seen[e.Key] = true
		out = append(out, e)
	}
	add(KeyEntry{Name: p.KeyName, Key: p.Key})
	for _, e := range p.Keys {
		add(e)
	}
	return promote(out)
}

// setRing writes an ordered list back into the provider's key fields.
func (p *Provider) setRing(ring []KeyEntry) {
	p.Key, p.KeyName, p.Keys = "", "", nil
	if len(ring) == 0 {
		return
	}
	p.Key, p.KeyName = ring[0].Key, ring[0].Name
	if len(ring) > 1 {
		p.Keys = append([]KeyEntry(nil), ring[1:]...)
	}
}

// promote makes sure the first key is enabled. A switched-off key at the front
// moves back to second place and the first enabled key takes the front; if
// every key is off, the first one is switched back on.
func promote(ring []KeyEntry) []KeyEntry {
	if len(ring) == 0 || !ring[0].Off {
		return ring
	}
	for j := 1; j < len(ring); j++ {
		if !ring[j].Off {
			out := []KeyEntry{ring[j]}
			out = append(out, ring[:j]...)
			return append(out, ring[j+1:]...)
		}
	}
	ring[0].Off = false
	return ring
}

func enabledCount(ring []KeyEntry) int {
	n := 0
	for _, e := range ring {
		if !e.Off {
			n++
		}
	}
	return n
}

// indexRef finds the key with the given id, or -1.
func indexRef(ring []KeyEntry, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1
	}
	for i, e := range ring {
		if refOf(e.Key) == ref {
			return i
		}
	}
	return -1
}

func noKey(ref string) error {
	return fmt.Errorf("no key %q on this provider; magpie provider keys lists them", ref)
}

// editKeys applies change to a stored provider's keys and saves the result.
// Only the key fields of the stored record are touched.
func editKeys(id string, change func([]KeyEntry) ([]KeyEntry, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := normalize(f.Providers[i])
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break // only model picks: an account, checked below
		}
		ring, err := change(p.ring())
		if err != nil {
			return err
		}
		raw := f.Providers[i]
		raw.setRing(ring)
		f.Providers[i] = raw
		return store(f)
	}
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s signs in with an account, which has no API keys to manage", id)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. It is enabled and goes after the keys that
// are already there; on a provider with no key it becomes the first one.
func AddKey(id, name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the API key is empty")
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(ring []KeyEntry) ([]KeyEntry, error) {
		for _, e := range ring {
			if e.Key == key {
				return nil, fmt.Errorf("that key is already on this provider (%s)", Mask(key))
			}
		}
		return append(ring, KeyEntry{Name: name, Key: key}), nil
	})
}

// UseKey makes the given key the first one and enabled. The key that was first
// stays enabled and comes right after it.
func UseKey(id, ref string) error {
	return editKeys(id, func(ring []KeyEntry) ([]KeyEntry, error) {
		i := indexRef(ring, ref)
		if i < 0 {
			return nil, noKey(ref)
		}
		if i == 0 {
			return ring, nil
		}
		e := ring[i]
		e.Off = false
		out := []KeyEntry{e, ring[0]}
		out = append(out, ring[1:i]...)
		return append(out, ring[i+1:]...), nil
	})
}

// SetKeyOn enables or disables a key. Switching off the first key hands the
// front to the next enabled one. The last enabled key cannot be switched off.
func SetKeyOn(id, ref string, on bool) error {
	return editKeys(id, func(ring []KeyEntry) ([]KeyEntry, error) {
		i := indexRef(ring, ref)
		if i < 0 {
			return nil, noKey(ref)
		}
		wantOff := !on
		if ring[i].Off == wantOff {
			return ring, nil
		}
		if wantOff && enabledCount(ring) == 1 {
			return nil, errors.New("keep at least one key enabled: this is the last enabled key")
		}
		ring[i].Off = wantOff
		return promote(ring), nil
	})
}

// RemoveKey deletes a key. Removing the first key hands the front to the next
// enabled one. The last enabled key cannot be removed.
func RemoveKey(id, ref string) error {
	return editKeys(id, func(ring []KeyEntry) ([]KeyEntry, error) {
		i := indexRef(ring, ref)
		if i < 0 {
			return nil, noKey(ref)
		}
		if !ring[i].Off && enabledCount(ring) == 1 {
			return nil, errors.New("cannot remove the last enabled key; enable another key first")
		}
		out := append(append([]KeyEntry(nil), ring[:i]...), ring[i+1:]...)
		return promote(out), nil
	})
}

// RenameKey names a key. The name is trimmed and may be cleared.
func RenameKey(id, ref, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(ring []KeyEntry) ([]KeyEntry, error) {
		i := indexRef(ring, ref)
		if i < 0 {
			return nil, noKey(ref)
		}
		ring[i].Name = name
		return ring, nil
	})
}

// KeyList lists all the provider's keys in the order they are tried.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, e := range p.ring() {
		out = append(out, KeyInfo{
			ID:     refOf(e.Key),
			Name:   e.Name,
			Masked: Mask(e.Key),
			Active: i == 0,
			On:     !e.Off,
		})
	}
	return out
}

// KeysOn lists the enabled keys in the order they are tried. A key that failed
// within the last minute goes to the back of the line. Disabled keys are never
// listed.
func (p Provider) KeysOn() []Credential {
	var ready, resting []Credential
	keyRests.Lock()
	defer keyRests.Unlock()
	now := time.Now()
	for _, e := range p.ring() {
		if e.Off {
			continue
		}
		c := Credential{ID: refOf(e.Key), Name: e.Name, Key: e.Key}
		if now.Before(keyRests.until[restKey(p.ID, c.ID)]) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

// MarkKeyFailed records that a key just failed, so the gateway tries the
// provider's other enabled keys first for the next minute.
func MarkKeyFailed(id, ref string) {
	keyRests.Lock()
	defer keyRests.Unlock()
	now := time.Now()
	for k, t := range keyRests.until {
		if now.After(t) {
			delete(keyRests.until, k)
		}
	}
	keyRests.until[restKey(id, ref)] = now.Add(keyRest)
}

func restKey(id, ref string) string {
	return strings.ToLower(strings.TrimSpace(id)) + "\x00" + strings.TrimSpace(ref)
}
