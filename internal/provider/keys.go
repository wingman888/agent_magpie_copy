package provider

// Several API keys per provider.
//
// The key in use is the provider's own "key" (with "keyName" and
// "keyProtocol"); the others live in "keys", in the order they were added.
// Listing them all gives one slice whose first entry is the key in use, so
// every operation below works on that slice and then writes it back with
// setKeys. The key in use is never disabled: disabling it hands the job to
// the next enabled key.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyEntry is one API key of a provider, with the name and protocol limit the
// user gave it. Protocol "" means the key may be used on any endpoint.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is a key's fingerprint: the first 5 bytes of the SHA-256 of the key,
// as 10 lowercase hex digits. It is what the CLI and the gateway call a key.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProto accepts the three endpoint protocols, or "" for any.
func validKeyProto(p Protocol) bool {
	switch p {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

// AllKeys lists every key the provider holds: the one in use first, then the
// others in order, enabled or not.
func (p Provider) AllKeys() []KeyEntry {
	var out []KeyEntry
	if p.Key != "" {
		out = append(out, KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// EnabledKeys lists the keys that may be tried, in order. Disabled keys are
// left out for good.
func (p Provider) EnabledKeys() []KeyEntry {
	var out []KeyEntry
	for _, k := range p.AllKeys() {
		if !k.Off && k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// WithKey is the provider as it is when it talks with key k.
func (p Provider) WithKey(k KeyEntry) Provider {
	p.Key = k.Key
	return p
}

// KeyFor is the provider set up with the key to use on an endpoint of the
// given protocol: the first enabled key that is unlimited or limited to that
// protocol. ok is false when every key is limited to other protocols. A
// provider with no keys at all (a local server) is returned as it is.
func (p Provider) KeyFor(proto Protocol) (Provider, bool) {
	if len(p.AllKeys()) == 0 {
		return p, true
	}
	for _, k := range p.EnabledKeys() {
		if k.Protocol == "" || k.Protocol == proto {
			return p.WithKey(k), true
		}
	}
	return p, false
}

// keyIndex finds a key in ks by its fingerprint, -1 when there is none.
func keyIndex(ks []KeyEntry, ref string) int {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, k := range ks {
		if KeyRef(k.Key) == ref {
			return i
		}
	}
	return -1
}

// firstEnabled finds the first enabled key at or after from, -1 when none.
func firstEnabled(ks []KeyEntry, from int) int {
	for i := from; i < len(ks); i++ {
		if !ks[i].Off {
			return i
		}
	}
	return -1
}

// setKeys stores a key list back on the provider: the first entry becomes the
// key in use, the rest the extra keys.
func setKeys(p *Provider, ks []KeyEntry) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(ks) == 0 {
		return
	}
	cur := ks[0]
	p.Key, p.KeyName, p.KeyProtocol = cur.Key, cur.Name, cur.Protocol
	if len(ks) > 1 {
		p.Keys = append([]KeyEntry(nil), ks[1:]...)
	}
}

// cleanKeys trims names and keys and drops entries with no key.
func cleanKeys(ks []KeyEntry) []KeyEntry {
	var out []KeyEntry
	for _, k := range ks {
		k.Name, k.Key = strings.TrimSpace(k.Name), strings.TrimSpace(k.Key)
		if k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// editKeys runs change on a stored provider and saves it when change succeeds.
func editKeys(id string, change func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		if err := change(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return saveAndTidy(f)
	}
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to change", id)
	}
	return fmt.Errorf("no provider %q", id)
}

func noKey(id, ref string) error {
	return fmt.Errorf("provider %s has no key %s", id, ref)
}

// AddKey saves another API key for a provider. It is enabled and goes after
// the keys already there; a provider with no key gets this one in use.
func AddKey(id, name, key string, proto Protocol) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("an API key is needed")
	}
	if !validKeyProto(proto) {
		return fmt.Errorf("protocol %q is not chat, responses or anthropic", proto)
	}
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		for _, k := range ks {
			if k.Key == key {
				return fmt.Errorf("that key is already saved for %s", p.Name)
			}
		}
		setKeys(p, append(ks, KeyEntry{Name: name, Key: key, Protocol: proto}))
		return nil
	})
}

// UseKey makes a key the one in use. The key it replaces stays enabled, right
// behind it; names and protocol limits travel with their keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		i := keyIndex(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		if i == 0 {
			return nil
		}
		cur := ks[i]
		cur.Off = false
		old := ks[0]
		old.Off = false
		out := []KeyEntry{cur, old}
		for k := 1; k < len(ks); k++ {
			if k != i {
				out = append(out, ks[k])
			}
		}
		setKeys(p, out)
		return nil
	})
}

// SetKeyOn enables or disables a key. Disabling the key in use hands the job
// to the next enabled key; the disabled one stays, just behind it. A provider
// must keep one enabled key, so the change is refused when there is none.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		i := keyIndex(ks, keyRef)
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
		if i == 0 {
			j := firstEnabled(ks, 1)
			if j < 0 {
				return errors.New("no other enabled key to take over; enable another key first")
			}
			ks[0].Off = true
			out := []KeyEntry{ks[j], ks[0]}
			for k := 1; k < len(ks); k++ {
				if k != j {
					out = append(out, ks[k])
				}
			}
			ks = out
		} else {
			ks[i].Off = true
		}
		setKeys(p, ks)
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot go; removing the key
// in use hands the job to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		i := keyIndex(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		var out []KeyEntry
		for k := range ks {
			if k != i {
				out = append(out, ks[k])
			}
		}
		if firstEnabled(out, 0) < 0 {
			return errors.New("the last enabled key cannot be removed; add or enable another key first")
		}
		if len(out) > 0 && out[0].Off {
			j := firstEnabled(out, 0)
			if j > 0 {
				first := out[j]
				out = append([]KeyEntry{first}, append(out[:j:j], out[j+1:]...)...)
			}
		}
		setKeys(p, out)
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		i := keyIndex(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		ks[i].Name = name
		setKeys(p, ks)
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProto(proto) {
		return fmt.Errorf("protocol %q is not chat, responses or anthropic", proto)
	}
	return editKeys(id, func(p *Provider) error {
		ks := p.AllKeys()
		i := keyIndex(ks, keyRef)
		if i < 0 {
			return noKey(id, keyRef)
		}
		ks[i].Protocol = proto
		setKeys(p, ks)
		return nil
	})
}
