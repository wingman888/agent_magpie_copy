package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is a key a provider keeps besides its current one. Off keys are
// kept on file but never tried.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Off      bool     `json:"off,omitempty"`
	Protocol Protocol `json:"protocol,omitempty"`
}

// KeyInfo describes one of a provider's keys for display. The full key
// never leaves this package through it: Masked is what the user sees.
type KeyInfo struct {
	Name     string
	Ref      string
	Masked   string
	Current  bool
	Off      bool
	Protocol Protocol
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in
// lowercase hex (ten characters). It is how the key commands name a key.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:5])
}

// ValidKeyProtocol reports whether a key may be limited to proto: one of the
// three wire protocols, or "" for any.
func ValidKeyProtocol(proto Protocol) bool {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

func badProtocol(proto Protocol) error {
	return fmt.Errorf("unknown protocol %q: use chat, responses or anthropic, or leave it empty for any", string(proto))
}

// keyList is the provider's keys in priority order: the current key first,
// then the others in the order they were kept.
func (p Provider) keyList() []ExtraKey {
	var out []ExtraKey
	if p.Key != "" {
		out = append(out, ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeyList stores an ordered list back on the provider; its first key is
// the current one.
func (p *Provider) setKeyList(list []ExtraKey) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(list) == 0 {
		return
	}
	p.Key, p.KeyName, p.KeyProtocol = list[0].Key, list[0].Name, list[0].Protocol
	if len(list) > 1 {
		p.Keys = append([]ExtraKey(nil), list[1:]...)
	}
}

// balanceKeys cleans a key list: it trims the entries, drops those with no
// key, and keeps the current key enabled. When the first key is off, the
// first enabled one moves to the front; if none is enabled, the first is
// turned back on.
func balanceKeys(list []ExtraKey) []ExtraKey {
	var out []ExtraKey
	for _, k := range list {
		k.Name = strings.TrimSpace(k.Name)
		k.Key = strings.TrimSpace(k.Key)
		if k.Key != "" {
			out = append(out, k)
		}
	}
	if len(out) > 0 && out[0].Off {
		i := 0
		for j := range out {
			if !out[j].Off {
				i = j
				break
			}
		}
		first := out[i]
		first.Off = false
		rest := append(append([]ExtraKey{}, out[:i]...), out[i+1:]...)
		out = append([]ExtraKey{first}, rest...)
	}
	return out
}

func enabledCount(list []ExtraKey) int {
	n := 0
	for _, k := range list {
		if !k.Off {
			n++
		}
	}
	return n
}

// keyIndex finds a key by fingerprint.
func keyIndex(list []ExtraKey, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, k := range list {
		if KeyRef(k.Key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no key %q on this provider", ref)
}

// validateKeys rejects a provider whose keys carry an unknown protocol or
// appear twice.
func validateKeys(p Provider) error {
	seen := map[string]bool{}
	for _, k := range p.keyList() {
		if !ValidKeyProtocol(k.Protocol) {
			return badProtocol(k.Protocol)
		}
		ref := KeyRef(k.Key)
		if seen[ref] {
			return errors.New("that key is already listed for this provider")
		}
		seen[ref] = true
	}
	return nil
}

// editKeys applies fn to the stored entry for id and writes the file back.
// Signed-in accounts have no keys of magpie's to manage.
func editKeys(id string, fn func([]ExtraKey) ([]ExtraKey, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account, which has no API keys to manage", id)
	}
	f := load()
	for i := range f.Providers {
		p := f.Providers[i]
		if p.ID != id || (p.Chat == "" && p.Responses == "" && p.Anthropic == "") {
			continue
		}
		list, err := fn(p.keyList())
		if err != nil {
			return err
		}
		p.setKeyList(balanceKeys(list))
		if err := validateKeys(p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q — magpie providers lists them", id)
}

// AddKey adds a key to a provider. A new key is enabled and goes after the
// keys already there. A provider with no key gets this one as its current key.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		if _, err := keyIndex(list, KeyRef(key)); err == nil {
			return nil, errors.New("that key is already added to this provider")
		}
		return append(list, ExtraKey{Name: name, Key: key, Protocol: proto}), nil
	})
}

// UseKey makes a key the current one. The key that was current stays
// enabled, right after the new current key; names, protocols and the
// other keys are kept.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			return list, nil
		}
		if list[i].Off {
			return nil, errors.New("that key is off; turn it on before making it current")
		}
		out := []ExtraKey{list[i], list[0]}
		for j := 1; j < len(list); j++ {
			if j != i {
				out = append(out, list[j])
			}
		}
		return out, nil
	})
}

// SetKeyOn turns a key on or off. The last enabled key cannot be turned off.
// Turning off the current key hands the role to the next enabled key, and
// the old current key is kept, off, at the front of the others.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		if on {
			list[i].Off = false
			return list, nil
		}
		if list[i].Off {
			return list, nil
		}
		if enabledCount(list) <= 1 {
			return nil, errors.New("that is the last enabled key; enable another key first")
		}
		if i != 0 {
			list[i].Off = true
			return list, nil
		}
		j := 0
		for k := 1; k < len(list); k++ {
			if !list[k].Off {
				j = k
				break
			}
		}
		old := list[0]
		old.Off = true
		out := []ExtraKey{list[j], old}
		for k := 1; k < len(list); k++ {
			if k != j {
				out = append(out, list[k])
			}
		}
		return out, nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be deleted. Deleting
// the current key hands the role to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		if !list[i].Off && enabledCount(list) <= 1 {
			return nil, errors.New("that is the last enabled key; it cannot be deleted")
		}
		out := append(append([]ExtraKey{}, list[:i]...), list[i+1:]...)
		return out, nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		list[i].Name = name
		return list, nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	return editKeys(id, func(list []ExtraKey) ([]ExtraKey, error) {
		i, err := keyIndex(list, keyRef)
		if err != nil {
			return nil, err
		}
		list[i].Protocol = proto
		return list, nil
	})
}

// KeyList describes the provider's keys in order; the first is the current one.
func (p Provider) KeyList() []KeyInfo {
	list := balanceKeys(p.keyList())
	out := make([]KeyInfo, 0, len(list))
	for i, k := range list {
		out = append(out, KeyInfo{
			Name:     k.Name,
			Ref:      KeyRef(k.Key),
			Masked:   Mask(k.Key),
			Current:  i == 0,
			Off:      k.Off,
			Protocol: k.Protocol,
		})
	}
	return out
}

// ResolveKey turns what the user typed into a key fingerprint: either the
// fingerprint itself, or a key name that is unique on this provider.
func (p Provider) ResolveKey(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	var byName []string
	for _, k := range balanceKeys(p.keyList()) {
		ref := KeyRef(k.Key)
		if ref == strings.ToLower(arg) {
			return ref, nil
		}
		if k.Name != "" && strings.EqualFold(k.Name, arg) {
			byName = append(byName, ref)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return "", fmt.Errorf("no key %q on %s — magpie provider %s lists them", arg, p.Name, p.ID)
	}
	return "", fmt.Errorf("more than one key is named %q; use its fingerprint", arg)
}
