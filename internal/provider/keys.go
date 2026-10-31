package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyEntry is a key besides the front one. Off keeps it but never uses it.
type KeyEntry struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
}

// KeyInfo describes one key without revealing it.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the front key
	On     bool   `json:"on"`     // enabled
}

// KeyUse is an enabled key as the gateway uses it.
type KeyUse struct {
	ID   string
	Name string
	Key  string
}

func keyID(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:8]
}

// KeyID is the stable short identifier of a key, derived from its content.
func KeyID(key string) string { return keyID(key) }

// normalizeKeys trims, drops empty and duplicate keys, and makes sure the
// front key is an enabled one.
func normalizeKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	if len(p.Keys) == 0 {
		p.Keys = nil
		return p
	}
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	var rest []KeyEntry
	for _, k := range p.Keys {
		k.Key, k.Name = strings.TrimSpace(k.Key), strings.TrimSpace(k.Name)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		rest = append(rest, k)
	}
	if p.Key == "" {
		for i, k := range rest {
			if !k.Off {
				p.Key, p.KeyName = k.Key, k.Name
				rest = append(rest[:i:i], rest[i+1:]...)
				break
			}
		}
	}
	p.Keys = rest
	if len(p.Keys) == 0 {
		p.Keys = nil
	}
	return p
}

// allKeys lists every key in try order, front first.
func (p Provider) allKeys() []KeyEntry {
	var out []KeyEntry
	if p.Key != "" {
		out = append(out, KeyEntry{Name: p.KeyName, Key: p.Key})
	}
	return append(out, p.Keys...)
}

// setKeys stores an ordered list back as front + others. The first enabled
// key becomes the front; disabled keys ahead of it keep their place after it.
func (p *Provider) setKeys(ks []KeyEntry) {
	front := -1
	for i, k := range ks {
		if !k.Off {
			front = i
			break
		}
	}
	p.Key, p.KeyName, p.Keys = "", "", nil
	if front < 0 {
		p.Keys = append([]KeyEntry(nil), ks...)
		return
	}
	p.Key, p.KeyName = ks[front].Key, ks[front].Name
	p.Keys = append(p.Keys, ks[:front]...)
	p.Keys = append(p.Keys, ks[front+1:]...)
	if len(p.Keys) == 0 {
		p.Keys = nil
	}
}

// KeyList describes every key in try order, masked.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	for i, k := range p.allKeys() {
		on := !k.Off
		if i == 0 && p.Key != "" {
			on = true
		}
		out = append(out, KeyInfo{ID: keyID(k.Key), Name: k.Name, Masked: Mask(k.Key), Active: i == 0 && p.Key != "", On: on})
	}
	return out
}

// KeysOn lists the enabled keys in try order.
func (p Provider) KeysOn() []KeyUse {
	var out []KeyUse
	for i, k := range p.allKeys() {
		if k.Off && !(i == 0 && p.Key != "") {
			continue
		}
		out = append(out, KeyUse{ID: keyID(k.Key), Name: k.Name, Key: k.Key})
	}
	return out
}

func enabledCount(ks []KeyEntry) int {
	n := 0
	for _, k := range ks {
		if !k.Off {
			n++
		}
	}
	return n
}

func indexOfKey(ks []KeyEntry, ref string) int {
	ref = strings.TrimSpace(ref)
	for i, k := range ks {
		if keyID(k.Key) == ref {
			return i
		}
	}
	return -1
}

// editKeys runs fn on the stored provider's ordered keys and saves the result.
func editKeys(id string, fn func(p *Provider, ks []KeyEntry) ([]KeyEntry, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s uses a signed-in account, not API keys", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := normalize(f.Providers[i])
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			break
		}
		ks, err := fn(&p, p.allKeys())
		if err != nil {
			return err
		}
		p.setKeys(ks)
		f.Providers[i] = normalize(p)
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

var errNoKey = errors.New("no such key")

// AddKey adds an enabled key after the existing ones.
func AddKey(id, name, key string) error {
	key, name = strings.TrimSpace(key), strings.TrimSpace(name)
	if key == "" {
		return errors.New("the API key is empty")
	}
	return editKeys(id, func(p *Provider, ks []KeyEntry) ([]KeyEntry, error) {
		if indexOfKey(ks, keyID(key)) >= 0 {
			return nil, errors.New("this key is already saved for the provider")
		}
		return append(ks, KeyEntry{Name: name, Key: key}), nil
	})
}

// UseKey makes a key the front one and enables it.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider, ks []KeyEntry) ([]KeyEntry, error) {
		i := indexOfKey(ks, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		k := ks[i]
		k.Off = false
		out := []KeyEntry{k}
		for j, o := range ks {
			if j != i {
				out = append(out, o)
			}
		}
		if i == 0 && p.Key != "" {
			return ks, nil
		}
		return out, nil
	})
}

// SetKeyOn enables or disables a key; one enabled key always remains.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider, ks []KeyEntry) ([]KeyEntry, error) {
		i := indexOfKey(ks, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		if !on && enabledCount(ks) == 1 && !ks[i].Off {
			return nil, errors.New("at least one key must stay enabled")
		}
		ks[i].Off = !on
		return ks, nil
	})
}

// RemoveKey deletes a key, keeping at least one enabled.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider, ks []KeyEntry) ([]KeyEntry, error) {
		i := indexOfKey(ks, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		if !ks[i].Off && enabledCount(ks) == 1 {
			return nil, errors.New("at least one key must stay enabled")
		}
		return append(ks[:i:i], ks[i+1:]...), nil
	})
}

// RenameKey names a key; an empty name clears it.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, func(p *Provider, ks []KeyEntry) ([]KeyEntry, error) {
		i := indexOfKey(ks, keyRef)
		if i < 0 {
			return nil, errNoKey
		}
		ks[i].Name = strings.TrimSpace(name)
		return ks, nil
	})
}
