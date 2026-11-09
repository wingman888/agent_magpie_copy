package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ExtraKey is a key saved on a provider besides the current one. Off keys
// are kept but never tried.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" means any protocol
	Off      bool     `json:"off,omitempty"`
}

// Credential is one usable key of a provider, in the order magpie tries them.
type Credential struct {
	Ref      string   // fingerprint, see KeyRef; "" for a keyless provider
	Name     string   // the user's label, may be empty
	Key      string   // the key itself; "" for a keyless provider
	Protocol Protocol // the one protocol it may serve; "" means any
}

// KeyRef is a key's fingerprint, the handle the commands use: the first five
// bytes of its SHA-256, as lowercase hex (ten characters).
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProtocol accepts the three protocols a key can be limited to, or
// "" for no limit.
func validKeyProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

func checkProtocol(p Protocol) error {
	if !validKeyProtocol(p) {
		return fmt.Errorf("key protocol %q must be chat, responses, anthropic, or empty for any", string(p))
	}
	return nil
}

// checkKeys rejects protocol limits magpie cannot honour and a key listed
// twice on one provider.
func checkKeys(p Provider) error {
	if err := checkProtocol(p.KeyProtocol); err != nil {
		return err
	}
	seen := map[string]bool{}
	if p.Key != "" {
		seen[p.Key] = true
	}
	for _, k := range p.Keys {
		if err := checkProtocol(k.Protocol); err != nil {
			return err
		}
		if seen[k.Key] {
			return errors.New("the same key is saved twice on one provider")
		}
		seen[k.Key] = true
	}
	return nil
}

// cleanKeys trims the labels and keys and drops entries without a key.
func cleanKeys(ks []ExtraKey) []ExtraKey {
	var out []ExtraKey
	for _, k := range ks {
		k.Name, k.Key = strings.TrimSpace(k.Name), strings.TrimSpace(k.Key)
		if k.Key == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// keepKeys carries the extra keys over when a provider is saved without
// them. A save with the same current key keeps the labels and extras it
// did not set. A save that changes the current key moves the old one to the
// front of the extras, so no key is lost silently.
func keepKeys(old, p Provider) Provider {
	if old.Key == "" && len(old.Keys) == 0 {
		return p
	}
	if p.Key == old.Key {
		if p.Keys == nil {
			p.Keys = old.Keys
		}
		if p.KeyName == "" {
			p.KeyName = old.KeyName
		}
		if p.KeyProtocol == "" {
			p.KeyProtocol = old.KeyProtocol
		}
		return p
	}
	extras := p.Keys
	if extras == nil {
		extras = old.Keys
	}
	if old.Key != "" {
		extras = append([]ExtraKey{{Name: old.KeyName, Key: old.Key, Protocol: old.KeyProtocol}}, extras...)
	}
	var keep []ExtraKey
	seen := map[string]bool{p.Key: true}
	for _, k := range extras {
		if seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		keep = append(keep, k)
	}
	p.Keys = keep
	return p
}

// allKeys lists every saved key of a provider, the current one first.
func (p Provider) allKeys() []string {
	var out []string
	if p.Key != "" {
		out = append(out, p.Key)
	}
	for _, k := range p.Keys {
		out = append(out, k.Key)
	}
	return out
}

// enabledKeys counts the keys magpie may try.
func (p Provider) enabledKeys() int {
	n := 0
	if p.Key != "" {
		n++
	}
	for _, k := range p.Keys {
		if !k.Off {
			n++
		}
	}
	return n
}

// Credentials lists the enabled keys in the order magpie tries them: the
// current key, then the extras in the order they were added. A provider
// with no key at all (a local server) gets one empty credential.
func (p Provider) Credentials() []Credential {
	var out []Credential
	if p.Key != "" {
		out = append(out, Credential{Ref: KeyRef(p.Key), Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			out = append(out, Credential{Ref: KeyRef(k.Key), Name: k.Name, Key: k.Key, Protocol: k.Protocol})
		}
	}
	if len(out) == 0 && len(p.Keys) == 0 {
		out = []Credential{{}}
	}
	return out
}

// Serves reports whether the credential may be sent to the provider's
// endpoint for proto: its protocol limit allows it, and the provider has
// that endpoint.
func (c Credential) Serves(p Provider, proto Protocol) bool {
	return (c.Protocol == "" || c.Protocol == proto) && p.Base(proto) != ""
}

// CredentialsFor lists the enabled credentials that may be sent to proto.
func (p Provider) CredentialsFor(proto Protocol) []Credential {
	var out []Credential
	for _, c := range p.Credentials() {
		if c.Serves(p, proto) {
			out = append(out, c)
		}
	}
	return out
}

// WithCredential is the provider as it looks when it sends one credential.
func (p Provider) WithCredential(c Credential) Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = c.Key, c.Name, c.Protocol, nil
	return p
}

// keyIndex finds a key by its fingerprint: -1 for the current key, else the
// index in Keys.
func keyIndex(p Provider, keyRef string) (int, bool) {
	if p.Key != "" && KeyRef(p.Key) == keyRef {
		return -1, true
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == keyRef {
			return i, true
		}
	}
	return 0, false
}

// firstEnabled is the index of the first enabled extra key, or -1.
func firstEnabled(ks []ExtraKey) int {
	for i, k := range ks {
		if !k.Off {
			return i
		}
	}
	return -1
}

func without(ks []ExtraKey, i int) []ExtraKey {
	out := make([]ExtraKey, 0, len(ks))
	out = append(out, ks[:i]...)
	return append(out, ks[i+1:]...)
}

// editKeys applies a change to one stored provider and saves the file. The
// stored entry is edited as it is, not normalized, so preset defaults do not
// leak into providers.json.
func editKeys(id string, fn func(p *Provider) error) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if _, ok := find(Accounts(), p.ID); ok {
		return fmt.Errorf("%s is a signed-in account; its keys belong to the agent", p.Name)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			if err := fn(&f.Providers[i]); err != nil {
				return err
			}
			return store(f)
		}
	}
	return fmt.Errorf("no provider %q", id)
}

func noKey(keyRef string) error {
	return fmt.Errorf("no key %s on this provider; magpie provider shows the keys", keyRef)
}

// AddKey saves another key on a provider. A provider with no key takes this
// one as its current key. A new key is enabled and goes after the others.
func AddKey(id, name, key string, proto Protocol) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if err := checkProtocol(proto); err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		for _, k := range p.allKeys() {
			if k == key {
				return errors.New("that key is already saved on this provider")
			}
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, ExtraKey{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled and
// goes first among the extras. Labels and protocol limits travel with keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, ok := keyIndex(*p, keyRef)
		if !ok {
			return noKey(keyRef)
		}
		if i < 0 {
			return nil
		}
		next := p.Keys[i]
		rest := without(p.Keys, i)
		if p.Key != "" {
			rest = append([]ExtraKey{{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}}, rest...)
		}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = rest
		return nil
	})
}

// SetKeyOn enables or disables a key. The current key cannot be disabled
// unless another enabled key takes its place; that key becomes current and
// the disabled one stays, first among the extras.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		i, ok := keyIndex(*p, keyRef)
		if !ok {
			return noKey(keyRef)
		}
		if i >= 0 {
			p.Keys[i].Off = !on
			return nil
		}
		if on {
			return nil
		}
		nxt := firstEnabled(p.Keys)
		if nxt < 0 {
			return errors.New("that is the only enabled key; enable another key first")
		}
		cur := ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}
		n := p.Keys[nxt]
		p.Keys = append([]ExtraKey{cur}, without(p.Keys, nxt)...)
		p.Key, p.KeyName, p.KeyProtocol = n.Key, n.Name, n.Protocol
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be removed. Removing
// the current key hands it to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, ok := keyIndex(*p, keyRef)
		if !ok {
			return noKey(keyRef)
		}
		off := i >= 0 && p.Keys[i].Off
		if !off && p.enabledKeys() <= 1 {
			return errors.New("that is the last enabled key; add or enable another before removing it")
		}
		if i >= 0 {
			p.Keys = without(p.Keys, i)
			return nil
		}
		nxt := firstEnabled(p.Keys)
		n := p.Keys[nxt]
		p.Keys = without(p.Keys, nxt)
		p.Key, p.KeyName, p.KeyProtocol = n.Key, n.Name, n.Protocol
		return nil
	})
}

// RenameKey changes a key's label.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		i, ok := keyIndex(*p, keyRef)
		if !ok {
			return noKey(keyRef)
		}
		if i < 0 {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if err := checkProtocol(proto); err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		i, ok := keyIndex(*p, keyRef)
		if !ok {
			return noKey(keyRef)
		}
		if i < 0 {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}
