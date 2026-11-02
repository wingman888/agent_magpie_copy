package provider

// Several API keys per provider. The first key lives in Provider.Key (the one
// "in use"); further keys sit in Provider.Keys. Every key that is not switched
// off is a candidate for a request, in order, so a key that runs out of quota
// hands over to the next. A key may be limited to one wire protocol.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Key is the short name for an additional key.
type Key = ExtraKey

// ExtraKey is a key besides the first one.
type ExtraKey struct {
	Key      string   `json:"key"`
	Name     string   `json:"name,omitempty"`
	Protocol Protocol `json:"protocol,omitempty"` // "" = any
	Off      bool     `json:"off,omitempty"`      // switched off: never tried
}

// KeyInfo is one key of a provider, whichever slot it sits in.
type KeyInfo struct {
	Key      string
	Name     string
	Protocol Protocol
	Off      bool
	Ref      string // KeyRef(Key)
}

// KeyRef is a key's fingerprint: the first 5 bytes of its SHA-256, in hex.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// ValidKeyProtocol reports whether p may limit a key: chat, responses,
// anthropic, or empty for any.
func ValidKeyProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

func checkKeyProtocols(p Provider) error {
	if !ValidKeyProtocol(p.KeyProtocol) {
		return fmt.Errorf("unknown key protocol %q (chat, responses, anthropic, or empty)", p.KeyProtocol)
	}
	for _, k := range p.Keys {
		if !ValidKeyProtocol(k.Protocol) {
			return fmt.Errorf("unknown key protocol %q (chat, responses, anthropic, or empty)", k.Protocol)
		}
	}
	return nil
}

// AllKeys lists every key of the provider in order, the in-use one first,
// switched-off ones included.
func (p Provider) AllKeys() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{Key: p.Key, Name: p.KeyName, Protocol: p.KeyProtocol, Ref: KeyRef(p.Key)})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{Key: k.Key, Name: k.Name, Protocol: k.Protocol, Off: k.Off, Ref: KeyRef(k.Key)})
	}
	return out
}

// ActiveKeys lists the keys that are switched on, in the order they are tried.
func (p Provider) ActiveKeys() []KeyInfo {
	var out []KeyInfo
	for _, k := range p.AllKeys() {
		if !k.Off {
			out = append(out, k)
		}
	}
	return out
}

// WithKey returns a copy of the provider that holds only the given key.
func (p Provider) WithKey(k KeyInfo) Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = k.Key, k.Name, k.Protocol, nil
	return p
}

// Only returns a copy of the provider that serves just the given protocols;
// the base URLs of the others are dropped.
func (p Provider) Only(protos ...Protocol) Provider {
	keep := func(x Protocol) bool {
		for _, q := range protos {
			if q == x {
				return true
			}
		}
		return false
	}
	if !keep(Chat) {
		p.Chat = ""
	}
	if !keep(Responses) {
		p.Responses = ""
	}
	if !keep(Anthropic) {
		p.Anthropic = ""
	}
	return p
}

// setKeys stores an ordered key list back into the provider. The first
// enabled key becomes Key; the rest (switched-off ones included) keep their
// relative order in Keys.
func setKeys(p *Provider, list []KeyInfo) {
	first := -1
	for i, k := range list {
		if !k.Off {
			first = i
			break
		}
	}
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	for i, k := range list {
		if i == first {
			p.Key, p.KeyName, p.KeyProtocol = k.Key, k.Name, k.Protocol
			continue
		}
		p.Keys = append(p.Keys, ExtraKey{Key: k.Key, Name: k.Name, Protocol: k.Protocol, Off: k.Off})
	}
}

// normalizeKeys cleans the key fields: whitespace trimmed, empty and repeated
// keys dropped, unknown protocols cleared, and an empty Key replaced by the
// first extra key that is on.
func normalizeKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	if !ValidKeyProtocol(p.KeyProtocol) {
		p.KeyProtocol = ""
	}
	if p.Key == "" && len(p.Keys) == 0 {
		return p
	}
	var list []KeyInfo
	seen := map[string]bool{}
	if p.Key != "" {
		list = append(list, KeyInfo{Key: p.Key, Name: p.KeyName, Protocol: p.KeyProtocol})
		seen[p.Key] = true
	}
	for _, k := range p.Keys {
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		proto := k.Protocol
		if !ValidKeyProtocol(proto) {
			proto = ""
		}
		list = append(list, KeyInfo{Key: k.Key, Name: strings.TrimSpace(k.Name), Protocol: proto, Off: k.Off})
	}
	if p.Key == "" {
		setKeys(&p, list)
		return p
	}
	// Key is always on; keep the order as given.
	p.Keys = nil
	for _, k := range list[1:] {
		p.Keys = append(p.Keys, ExtraKey{Key: k.Key, Name: k.Name, Protocol: k.Protocol, Off: k.Off})
	}
	return p
}

// editable finds a provider whose keys the user may manage.
func editable(id string) (*Provider, error) {
	p, err := Find(id)
	if err != nil {
		return nil, err
	}
	if p.Account != nil {
		return nil, fmt.Errorf("%s is a signed-in account; it has no API keys", p.Name)
	}
	return p, nil
}

// indexOfKey finds a key by its fingerprint (or its text).
func indexOfKey(list []KeyInfo, ref string) int {
	ref = strings.TrimSpace(ref)
	for i, k := range list {
		if k.Ref == ref || k.Key == ref {
			return i
		}
	}
	return -1
}

func editKeys(id, ref string, edit func(list []KeyInfo, i int) ([]KeyInfo, error)) error {
	p, err := editable(id)
	if err != nil {
		return err
	}
	list := p.AllKeys()
	i := indexOfKey(list, ref)
	if i < 0 {
		return fmt.Errorf("%s has no key %q", p.Name, ref)
	}
	list, err = edit(list, i)
	if err != nil {
		return err
	}
	setKeys(p, list)
	return Save(*p)
}

func enabledCount(list []KeyInfo) int {
	n := 0
	for _, k := range list {
		if !k.Off {
			n++
		}
	}
	return n
}

// AddKey adds a key to a provider, after the ones it has; it is on. A
// provider without a key gets it as its first.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("a key cannot be empty")
	}
	if !ValidKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q (chat, responses, anthropic, or empty)", proto)
	}
	p, err := editable(id)
	if err != nil {
		return err
	}
	list := p.AllKeys()
	if indexOfKey(list, key) >= 0 {
		return fmt.Errorf("%s already has that key", p.Name)
	}
	list = append(list, KeyInfo{Key: key, Name: strings.TrimSpace(name), Protocol: proto})
	if p.Key == "" {
		// no key yet: the new one leads, whatever switched-off ones follow
		last := list[len(list)-1]
		list = append([]KeyInfo{last}, list[:len(list)-1]...)
	}
	setKeys(p, list)
	return Save(*p)
}

// UseKey makes a key the one in use. The key that was in use stays on,
// right behind it.
func UseKey(id, keyRef string) error {
	return editKeys(id, keyRef, func(list []KeyInfo, i int) ([]KeyInfo, error) {
		k := list[i]
		k.Off = false
		out := []KeyInfo{k}
		out = append(out, list[:i]...)
		out = append(out, list[i+1:]...)
		// the former first key may have been listed off only if it was not first
		return out, nil
	})
}

// SetKeyOn switches a key on or off. The last key that is on cannot be
// switched off. Switching off the first key passes the role to the next one
// that is on; the old one stays, off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, keyRef, func(list []KeyInfo, i int) ([]KeyInfo, error) {
		if list[i].Off == !on {
			return list, nil
		}
		if !on && enabledCount(list) <= 1 {
			return nil, errors.New("the last key that is on cannot be switched off")
		}
		list[i].Off = !on
		return list, nil
	})
}

// RemoveKey deletes a key. The last key that is on cannot be deleted.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, keyRef, func(list []KeyInfo, i int) ([]KeyInfo, error) {
		if !list[i].Off && enabledCount(list) <= 1 {
			return nil, errors.New("the last key that is on cannot be deleted")
		}
		return append(list[:i:i], list[i+1:]...), nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, keyRef, func(list []KeyInfo, i int) ([]KeyInfo, error) {
		list[i].Name = strings.TrimSpace(name)
		return list, nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit ("").
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !ValidKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q (chat, responses, anthropic, or empty)", proto)
	}
	return editKeys(id, keyRef, func(list []KeyInfo, i int) ([]KeyInfo, error) {
		list[i].Protocol = proto
		return list, nil
	})
}
