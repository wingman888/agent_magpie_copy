package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Several API keys per provider. The provider's own Key, KeyName and
// KeyProtocol are the key in use; Keys holds the others, in the order they
// were added, each possibly turned off. The key in use is always enabled, so
// a provider always has at least one key on while it has any.
//
// A key may be limited to one protocol (KeyProtocol): it then reaches only
// that protocol's endpoint. Candidates lists the keys a request may go out
// on, in the order to try them.

// ExtraKey is an API key besides the one in use.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is the fingerprint that names a key from the outside: the first
// five bytes of its SHA-256, as ten lower-case hex digits.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// ValidKeyProtocol reports whether a key may be limited to proto. Empty means
// the key may be used on any endpoint.
func ValidKeyProtocol(proto Protocol) bool {
	return proto == "" || proto == Chat || proto == Responses || proto == Anthropic
}

// allKeys lists every key of p, the one in use first, disabled ones included.
func allKeys(p Provider) []ExtraKey {
	var out []ExtraKey
	if p.Key != "" {
		out = append(out, ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeys stores ks as the keys of p; the first one becomes the key in use.
func setKeys(p *Provider, ks []ExtraKey) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(ks) == 0 {
		return
	}
	p.Key, p.KeyName, p.KeyProtocol = ks[0].Key, ks[0].Name, ks[0].Protocol
	if len(ks) > 1 {
		p.Keys = append([]ExtraKey(nil), ks[1:]...)
	}
}

func indexKey(ks []ExtraKey, ref string) int {
	for i, k := range ks {
		if KeyRef(k.Key) == ref {
			return i
		}
	}
	return -1
}

// nextEnabled finds the first enabled key at or after from, or -1.
func nextEnabled(ks []ExtraKey, from int) int {
	for i := from; i < len(ks); i++ {
		if !ks[i].Off {
			return i
		}
	}
	return -1
}

// storedProvider finds a provider as it sits in the file, unnormalized.
func storedProvider(id string) (Provider, bool) {
	for _, p := range load().Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// updateStored applies fn to a stored provider and saves the file.
func updateStored(id string, fn func(p *Provider) error) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return fmt.Errorf("%s is a signed-in account; it has no API keys of its own", id)
		}
		if err := fn(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

func noKey(id, ref string) error {
	return fmt.Errorf("%s has no key %s", id, ref)
}

func badProtocol(proto Protocol) error {
	return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic or nothing", string(proto))
}

// AddKey adds an API key to a provider. A provider with no key gets this one
// as the key in use; otherwise the key is enabled and waits behind the ones
// before it.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		for _, k := range ks {
			if k.Key == key {
				return fmt.Errorf("that key is already on %s", id)
			}
		}
		if name == "" {
			name = fmt.Sprintf("key %d", len(ks)+1)
		}
		setKeys(p, append(ks, ExtraKey{Name: name, Key: key, Protocol: proto}))
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit when proto
// is empty.
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		i := indexKey(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		ks[i].Protocol = proto
		setKeys(p, ks)
		return nil
	})
}

// UseKey makes a key the one in use. The key it replaces stays on, right
// behind it, with its name and protocol.
func UseKey(id, keyRef string) error {
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		i := indexKey(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		if i == 0 {
			return nil
		}
		t := ks[i]
		t.Off = false
		out := []ExtraKey{t, ks[0]}
		out = append(out, ks[1:i]...)
		out = append(out, ks[i+1:]...)
		setKeys(p, out)
		return nil
	})
}

// SetKeyOn turns a key on or off. Turning off the key in use hands the job to
// the next enabled key, and the old one stays behind it, turned off. A
// provider keeps at least one key on.
func SetKeyOn(id, keyRef string, on bool) error {
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		i := indexKey(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		if on {
			ks[i].Off = false
			setKeys(p, ks)
			return nil
		}
		if ks[i].Off {
			return nil
		}
		if i > 0 {
			ks[i].Off = true
			setKeys(p, ks)
			return nil
		}
		j := nextEnabled(ks, 1)
		if j < 0 {
			return fmt.Errorf("%s needs one key on: %s is the last one", id, keyName(ks[0]))
		}
		old := ks[0]
		old.Off = true
		nk := ks[j]
		nk.Off = false
		out := []ExtraKey{nk, old}
		out = append(out, ks[1:j]...)
		out = append(out, ks[j+1:]...)
		setKeys(p, out)
		return nil
	})
}

// RemoveKey deletes a key. The last key that is on cannot be removed; removing
// the key in use hands the job to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		i := indexKey(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		if i > 0 {
			setKeys(p, append(ks[:i:i], ks[i+1:]...))
			return nil
		}
		j := nextEnabled(ks, 1)
		if j < 0 {
			return fmt.Errorf("%s needs one key on: %s is the last one", id, keyName(ks[0]))
		}
		nk := ks[j]
		nk.Off = false
		out := []ExtraKey{nk}
		out = append(out, ks[1:j]...)
		out = append(out, ks[j+1:]...)
		setKeys(p, out)
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the key needs a name")
	}
	return updateStored(id, func(p *Provider) error {
		ks := allKeys(*p)
		i := indexKey(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		ks[i].Name = name
		setKeys(p, ks)
		return nil
	})
}

func keyName(k ExtraKey) string {
	if k.Name != "" {
		return k.Name
	}
	return KeyRef(k.Key)
}

// cleanKeys trims the extra keys, drops empty ones and repeats of the key in
// use or of each other, and returns nil when none is left.
func cleanKeys(p Provider) []ExtraKey {
	seen := map[string]bool{p.Key: true}
	var out []ExtraKey
	for _, k := range p.Keys {
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

// checkKeys rejects a key limited to a protocol magpie cannot speak.
func checkKeys(p Provider) error {
	if !ValidKeyProtocol(p.KeyProtocol) {
		return badProtocol(p.KeyProtocol)
	}
	for _, k := range p.Keys {
		if !ValidKeyProtocol(k.Protocol) {
			return badProtocol(k.Protocol)
		}
	}
	return nil
}

// carryKeys keeps the other keys of a stored provider that is saved again
// without them. The key that stays in use keeps its name and protocol; a key
// the user replaced is dropped.
func carryKeys(old Provider, p *Provider) {
	if len(p.Keys) > 0 {
		return
	}
	var rest []ExtraKey
	var match *ExtraKey
	for i, k := range allKeys(old) {
		if p.Key != "" && k.Key == p.Key {
			kk := k
			match = &kk
			continue
		}
		if i == 0 {
			continue // the key this one replaced
		}
		rest = append(rest, k)
	}
	if match != nil {
		if p.KeyName == "" {
			p.KeyName = match.Name
		}
		if p.KeyProtocol == "" {
			p.KeyProtocol = match.Protocol
		}
	}
	if p.Key == "" {
		if j := nextEnabled(rest, 0); j >= 0 {
			first := rest[j]
			p.Key, p.KeyName, p.KeyProtocol = first.Key, first.Name, first.Protocol
			rest = append(rest[:j:j], rest[j+1:]...)
		}
	}
	p.Keys = rest
}

// withKey is p with one key in use, the way Sign, Fetch and Test read it.
func (p Provider) withKey(k ExtraKey) Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = k.Key, k.Name, k.Protocol, nil
	return p
}

// Candidates lists the ways to send a request for model, which arrived in the
// client's protocol from, in the order to try them. Each is a copy of p with
// one enabled key in use; a key limited to a protocol the provider lacks is
// left out. A provider without keys is its own only candidate.
func (p Provider) Candidates(model string, from Protocol) []Provider {
	ks := allKeys(p)
	if p.Account != nil || len(ks) == 0 {
		return []Provider{p}
	}
	speaks := p.Speaks()
	var out []Provider
	for _, k := range ks {
		if k.Off {
			continue
		}
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue
		}
		if k.Protocol == "" && len(speaks) == 0 {
			continue
		}
		out = append(out, p.withKey(k))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return keyRank(model, from, out[i].KeyProtocol) < keyRank(model, from, out[j].KeyProtocol)
	})
	return out
}

// keyRank orders keys for a model. A Claude model prefers the Anthropic key,
// a GPT, o-series or codex model the OpenAI-style keys; then a key that is
// not limited. Other models prefer keys that speak the client's protocol, so
// nothing needs translating. A poorer fit comes later, as a last resort.
func keyRank(model string, from, kp Protocol) int {
	switch modelFamily(model) {
	case "claude":
		switch kp {
		case Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case "openai":
		switch kp {
		case Chat, Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if kp == "" || kp == from {
		return 0
	}
	return 1
}

// modelFamily sorts a model id into "claude", "openai" or "" (other), looking
// at the part after the last slash.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"):
		return "openai"
	case len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9':
		return "openai"
	}
	return ""
}
