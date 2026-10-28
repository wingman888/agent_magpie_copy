package provider

// A provider can hold several API keys, for example a personal and a team
// account of one vendor. Key (named KeyName) is the one requests go to first;
// Keys are the others in the order they take over, each switched on or off.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyAccount is a saved key beyond the front one.
type KeyAccount struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"` // saved but not used
}

// KeyInfo is a key as shown to the user: never the key itself.
type KeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // first in line
	On     bool   `json:"on"`
}

// KeyID is a key's fingerprint, what names it in the calls below.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// cleanKeys trims the saved keys and drops empty ones and repeats, front included.
func cleanKeys(front string, keys []KeyAccount) []KeyAccount {
	var out []KeyAccount
	seen := map[string]bool{}
	if front != "" {
		seen[KeyID(front)] = true
	}
	for _, k := range keys {
		k.Name, k.Key = strings.TrimSpace(k.Name), strings.TrimSpace(k.Key)
		if k.Key == "" || seen[KeyID(k.Key)] {
			continue
		}
		seen[KeyID(k.Key)] = true
		out = append(out, k)
	}
	return out
}

// KeyList is every key in the order requests try them.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{ID: KeyID(p.Key), Name: p.KeyName, Masked: Mask(p.Key), Active: true, On: true})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{ID: KeyID(k.Key), Name: k.Name, Masked: Mask(k.Key), On: !k.Off})
	}
	return out
}

// KeysOn is the keys in use, first in line first.
func (p Provider) KeysOn() []KeyAccount {
	if p.Key == "" {
		return nil
	}
	out := []KeyAccount{{Name: p.KeyName, Key: p.Key}}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// keyProvider finds the provider to change.
func keyProvider(id string) (*Provider, error) {
	return Find(id)
}

var errNoKey = "%s has no such key"

// locate says where a key sits: the front (-1) or an index into Keys.
func (p *Provider) locate(ref string) (int, bool) {
	if p.Key != "" && KeyID(p.Key) == ref {
		return -1, true
	}
	for i, k := range p.Keys {
		if KeyID(k.Key) == ref {
			return i, true
		}
	}
	return 0, false
}

// nextOn is the index in Keys of the first key in use.
func (p *Provider) nextOn() int {
	for i, k := range p.Keys {
		if !k.Off && k.Key != "" {
			return i
		}
	}
	return -1
}

// promote makes Keys[i] the front key; the old front is returned to be
// placed by the caller.
func (p *Provider) promote(i int) {
	k := p.Keys[i]
	p.Keys = append(p.Keys[:i:i], p.Keys[i+1:]...)
	p.Key, p.KeyName = k.Key, k.Name
}

// AddKey saves another key for a provider, switched on and last in line.
func AddKey(id, name, key string) error {
	p, err := keyProvider(id)
	if err != nil {
		return err
	}
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("paste the key to add")
	}
	if p.Account != nil {
		return errors.New("a signed-in account has no keys")
	}
	for _, k := range p.KeyList() {
		if k.ID == KeyID(key) {
			return fmt.Errorf("%s already has this key", p.Name)
		}
	}
	if p.Key == "" {
		p.Key, p.KeyName = key, name
	} else {
		p.Keys = append(p.Keys, KeyAccount{Name: name, Key: key})
	}
	return Save(*p)
}

// UseKey puts a key first in line and switches it on; the key that was first
// takes the next place.
func UseKey(id, keyRef string) error {
	p, err := keyProvider(id)
	if err != nil {
		return err
	}
	i, ok := p.locate(keyRef)
	if !ok {
		return fmt.Errorf(errNoKey, p.Name)
	}
	if i < 0 {
		return nil
	}
	old := KeyAccount{Name: p.KeyName, Key: p.Key}
	p.promote(i)
	p.Keys = append([]KeyAccount{old}, p.Keys...)
	return Save(*p)
}

// SetKeyOn switches a key on or off. Switching the first key off hands its
// place to the next key in use.
func SetKeyOn(id, keyRef string, on bool) error {
	p, err := keyProvider(id)
	if err != nil {
		return err
	}
	i, ok := p.locate(keyRef)
	if !ok {
		return fmt.Errorf(errNoKey, p.Name)
	}
	if i >= 0 {
		p.Keys[i].Off = !on
		return Save(*p)
	}
	if on {
		return nil
	}
	n := p.nextOn()
	if n < 0 {
		return errors.New("that's the only key in use; turn another on first")
	}
	old := KeyAccount{Name: p.KeyName, Key: p.Key, Off: true}
	p.promote(n)
	p.Keys = append([]KeyAccount{old}, p.Keys...)
	return Save(*p)
}

// RemoveKey deletes a key. Removing the first key hands its place to the
// next key in use.
func RemoveKey(id, keyRef string) error {
	p, err := keyProvider(id)
	if err != nil {
		return err
	}
	i, ok := p.locate(keyRef)
	if !ok {
		return fmt.Errorf(errNoKey, p.Name)
	}
	if i >= 0 {
		p.Keys = append(p.Keys[:i:i], p.Keys[i+1:]...)
		return Save(*p)
	}
	n := p.nextOn()
	if n < 0 {
		return errors.New("that's the only key in use; turn another on before removing it")
	}
	p.promote(n)
	return Save(*p)
}

// RenameKey names a key.
func RenameKey(id, keyRef, name string) error {
	p, err := keyProvider(id)
	if err != nil {
		return err
	}
	i, ok := p.locate(keyRef)
	if !ok {
		return fmt.Errorf(errNoKey, p.Name)
	}
	name = strings.TrimSpace(name)
	if i < 0 {
		p.KeyName = name
	} else {
		p.Keys[i].Name = name
	}
	return Save(*p)
}
