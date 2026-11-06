package provider

// Several API keys per provider. The key in use is Provider.Key (named by
// KeyName, limited by KeyProtocol); the others sit in Provider.Keys in the
// order they were added. An Off key is kept but never tried. The first key
// in the list is always enabled, so a provider with any key always has one
// that works.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Protocol names for key limits. They are the same values as the protocols
// themselves; the Proto* names read well at call sites about keys.
const (
	ProtoChat      = Chat
	ProtoResponses = Responses
	ProtoAnthropic = Anthropic
)

// KeyEntry is an API key a provider holds besides its current one.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" serves any protocol
	Off      bool     `json:"off,omitempty"`      // kept, never tried
}

// KeyInfo describes one key of a provider, for display.
type KeyInfo struct {
	Ref      string   // the key's fingerprint, see KeyRef
	Name     string   // the user's name for it, may be empty
	Masked   string   // the key with its middle hidden
	Protocol Protocol // "" serves any protocol
	Current  bool     // the key in use
	On       bool     // tried by requests
}

// Credential is one key a request may be sent with, with the limit it carries.
type Credential struct {
	Ref      string
	Name     string
	Key      string
	Protocol Protocol
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in
// lowercase hex. It names a key on the command line and in the recent calls.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// ParseKeyProtocol reads a protocol limit the user typed: chat, responses,
// anthropic, or "" for any. Anything else is an error.
func ParseKeyProtocol(s string) (Protocol, error) {
	p := Protocol(strings.ToLower(strings.TrimSpace(s)))
	if !validKeyProtocol(p) {
		return "", fmt.Errorf("unknown protocol %q: use chat, responses, anthropic, or nothing for any", s)
	}
	return p, nil
}

func validKeyProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

// keyList is every key in order, the current one first.
func (p Provider) keyList() []KeyEntry {
	var out []KeyEntry
	if p.Key != "" {
		out = append(out, KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeys stores a key list back: the first is current, the rest are others.
func (p *Provider) setKeys(ks []KeyEntry) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(ks) == 0 {
		return
	}
	p.Key, p.KeyName, p.KeyProtocol = ks[0].Key, ks[0].Name, ks[0].Protocol
	if len(ks) > 1 {
		p.Keys = append([]KeyEntry(nil), ks[1:]...)
	}
}

// ListKeys lists every key of the provider, the current one first.
func (p Provider) ListKeys() []KeyInfo {
	var out []KeyInfo
	for i, k := range p.keyList() {
		out = append(out, KeyInfo{Ref: KeyRef(k.Key), Name: k.Name, Masked: Mask(k.Key),
			Protocol: k.Protocol, Current: i == 0, On: !k.Off})
	}
	return out
}

// Credentials lists the keys a request may use, enabled ones only, in the
// order they are tried. A provider with no key (a local server) has one
// empty credential.
func (p Provider) Credentials() []Credential {
	var out []Credential
	for _, k := range p.keyList() {
		if k.Off {
			continue
		}
		out = append(out, Credential{Ref: KeyRef(k.Key), Name: k.Name, Key: k.Key, Protocol: k.Protocol})
	}
	if len(out) == 0 {
		out = []Credential{{}}
	}
	return out
}

// cleanKeys drops empty and repeated keys from a stored list.
func cleanKeys(top string, ks []KeyEntry) []KeyEntry {
	seen := map[string]bool{top: true}
	var out []KeyEntry
	for _, k := range ks {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	return out
}

// keyed finds a provider that holds API keys. A signed-in account does not.
func keyed(id string) (*Provider, error) {
	p, err := Find(id)
	if err != nil {
		return nil, err
	}
	if p.Account != nil {
		return nil, fmt.Errorf("%s is a signed-in account and has no API keys", p.Name)
	}
	return p, nil
}

// findKey is the index of the key with the given fingerprint.
func findKey(ks []KeyEntry, ref string) (int, error) {
	for i, k := range ks {
		if KeyRef(k.Key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no key %q", ref)
}

// nextOn is the first enabled key at or after from, or -1.
func nextOn(ks []KeyEntry, from int) int {
	for j := from; j < len(ks); j++ {
		if !ks[j].Off {
			return j
		}
	}
	return -1
}

// AddKey adds a key to a provider. It is enabled and goes after the keys
// already there; a provider with no key gets this one as its current key.
func AddKey(id, name, key string, proto Protocol) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown protocol %q: use chat, responses, anthropic, or nothing for any", proto)
	}
	ks := p.keyList()
	for _, k := range ks {
		if k.Key == key {
			return fmt.Errorf("%s already has this key (%s)", p.Name, KeyRef(key))
		}
	}
	ks = append(ks, KeyEntry{Name: strings.TrimSpace(name), Key: key, Protocol: proto})
	p.setKeys(ks)
	return Save(*p)
}

// UseKey makes a key the one in use. The key it replaces stays enabled, right
// after it; the other keys and their limits are kept.
func UseKey(id, keyRef string) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	ks := p.keyList()
	i, err := findKey(ks, keyRef)
	if err != nil {
		return err
	}
	if i == 0 {
		return nil
	}
	cur := ks[i]
	cur.Off = false
	out := []KeyEntry{cur, ks[0]}
	out = append(out, ks[1:i]...)
	out = append(out, ks[i+1:]...)
	p.setKeys(out)
	return Save(*p)
}

// SetKeyOn enables or disables a key. Disabling the key in use hands the
// role to the next enabled key, and the old one stays on, disabled. The last
// enabled key cannot be disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	ks := p.keyList()
	i, err := findKey(ks, keyRef)
	if err != nil {
		return err
	}
	if on {
		if !ks[i].Off {
			return nil
		}
		ks[i].Off = false
		p.setKeys(ks)
		return Save(*p)
	}
	if ks[i].Off {
		return nil
	}
	var out []KeyEntry
	if i == 0 {
		j := nextOn(ks, 1)
		if j < 0 {
			return fmt.Errorf("%s has no other enabled key to take over; add one before turning this off", p.Name)
		}
		old := ks[0]
		old.Off = true
		out = []KeyEntry{ks[j], old}
		out = append(out, ks[1:j]...)
		out = append(out, ks[j+1:]...)
	} else {
		ks[i].Off = true
		out = ks
	}
	p.setKeys(out)
	return Save(*p)
}

// RemoveKey deletes a key. The last enabled key cannot be removed; removing
// the key in use hands the role to the next enabled key.
func RemoveKey(id, keyRef string) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	ks := p.keyList()
	i, err := findKey(ks, keyRef)
	if err != nil {
		return err
	}
	var out []KeyEntry
	if i == 0 {
		j := nextOn(ks, 1)
		if j < 0 {
			return fmt.Errorf("%s would have no enabled key left; add another first", p.Name)
		}
		out = []KeyEntry{ks[j]}
		out = append(out, ks[1:j]...)
		out = append(out, ks[j+1:]...)
	} else {
		out = append(out, ks[:i]...)
		out = append(out, ks[i+1:]...)
	}
	p.setKeys(out)
	return Save(*p)
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	ks := p.keyList()
	i, err := findKey(ks, keyRef)
	if err != nil {
		return err
	}
	ks[i].Name = strings.TrimSpace(name)
	p.setKeys(ks)
	return Save(*p)
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	p, err := keyed(id)
	if err != nil {
		return err
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown protocol %q: use chat, responses, anthropic, or nothing for any", proto)
	}
	ks := p.keyList()
	i, err := findKey(ks, keyRef)
	if err != nil {
		return err
	}
	ks[i].Protocol = proto
	p.setKeys(ks)
	return Save(*p)
}
