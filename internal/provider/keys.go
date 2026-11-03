package provider

// A provider can hold several accounts, as a subscription can: several API
// keys — a personal one and the team's, a paid plan and a free one. Any
// number of them can be on at once: requests go to the first, and when it
// is out of quota or rate limited the gateway moves on to the next one
// that's on (see gateway/fallback.go). Key stays the first, so everything
// else that talks to the vendor reads it as before.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeyAccount is a saved key after the first. Off keeps it without using it.
// Protocol, when set, limits the key to that protocol's endpoint: some
// relays hand out one key for their Anthropic endpoint and another for
// their OpenAI one. Empty means the key works for every endpoint.
type KeyAccount struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Off      bool     `json:"off,omitempty"`
	Protocol Protocol `json:"protocol,omitempty"`
}

// KeyInfo describes one of a provider's keys without giving it away.
type KeyInfo struct {
	ID     string `json:"id"` // a fingerprint, to name it in a switch
	Name   string `json:"name,omitempty"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the first, where requests go
	On     bool   `json:"on"`     // in use: the first, or next in line

	Protocol Protocol `json:"protocol,omitempty"` // the only protocol it's for; empty for all
}

func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// KeyID is the fingerprint a key is named by, in lists and in the gateway.
func KeyID(key string) string { return keyID(key) }

// KeyList is a provider's keys, in the order requests try them.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{ID: keyID(p.Key), Name: p.KeyName, Masked: Mask(p.Key), Active: true, On: true, Protocol: p.KeyProtocol})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{ID: keyID(k.Key), Name: k.Name, Masked: Mask(k.Key), On: !k.Off, Protocol: k.Protocol})
	}
	return out
}

// KeysOn is the keys in use, first to last: the first key, then every
// saved one that's on.
func (p Provider) KeysOn() []KeyAccount {
	if p.Key == "" {
		return nil
	}
	out := []KeyAccount{p.firstKey()}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// firstKey is the key in use, as a KeyAccount.
func (p Provider) firstKey() KeyAccount {
	return KeyAccount{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
}

// setFirst makes k the key in use, its name and protocol with it.
func (p *Provider) setFirst(k KeyAccount) {
	p.Key, p.KeyName, p.KeyProtocol = k.Key, k.Name, k.Protocol
}

// WithKey is the provider using k: its key, name and protocol. A key
// limited to one protocol leaves only that protocol's endpoint, so it is
// never sent anywhere else; a key for all keeps every endpoint.
func (p Provider) WithKey(k KeyAccount) Provider {
	p.setFirst(k)
	if k.Protocol != "" {
		for _, pr := range Protocols {
			if pr == k.Protocol {
				continue
			}
			switch pr {
			case Chat:
				p.Chat = ""
			case Responses:
				p.Responses = ""
			case Anthropic:
				p.Anthropic = ""
			}
		}
	}
	return p
}

// keyProtocol checks a protocol a key is limited to: empty, or one of the
// protocols magpie speaks upstream.
func keyProtocol(proto Protocol) (Protocol, error) {
	proto = Protocol(strings.ToLower(strings.TrimSpace(string(proto))))
	if proto == "" {
		return "", nil
	}
	for _, pr := range Protocols {
		if pr == proto {
			return proto, nil
		}
	}
	return "", fmt.Errorf("unknown protocol %q: a key is for chat, responses or anthropic, or for all", proto)
}

// AddKey saves one more key for a provider, on: it takes requests after
// the ones before it. It becomes the first when the provider has none.
// proto limits it to that protocol's endpoint; empty means all of them.
func AddKey(id, name, key string, proto Protocol) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if key == "" {
		return errors.New("paste the key to add")
	}
	proto, err := keyProtocol(proto)
	if err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return errors.New("a signed-in account has no keys")
	}
	for _, k := range p.KeyList() {
		if k.ID == keyID(key) {
			return fmt.Errorf("%s already has this key", p.Name)
		}
	}
	if p.Key == "" {
		p.setFirst(KeyAccount{Name: name, Key: key, Protocol: proto})
	} else {
		p.Keys = append(p.Keys, KeyAccount{Name: name, Key: key, Protocol: proto})
	}
	return Save(*p)
}

// SetKeyProtocol limits one of a provider's keys to one protocol's
// endpoint, or, given "", lets it be used for all of them again.
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	proto, err := keyProtocol(proto)
	if err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i < 0 {
		p.KeyProtocol = proto
	} else {
		p.Keys[i].Protocol = proto
	}
	return Save(*p)
}

// findKey is where a key is among p.Keys: -1 for the first key, and ok
// false when p has no such key.
func findKey(p *Provider, ref string) (int, bool) {
	if p.Key != "" && keyID(p.Key) == ref {
		return -1, true
	}
	for i, k := range p.Keys {
		if keyID(k.Key) == ref {
			return i, true
		}
	}
	return 0, false
}

// UseKey makes one of a provider's keys the first, and turns it on; the
// one it replaces stays on, next in line.
func UseKey(id, keyRef string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i < 0 {
		return nil
	}
	k := p.Keys[i]
	k.Off = false
	rest := append([]KeyAccount{p.firstKey()}, append(p.Keys[:i:i], p.Keys[i+1:]...)...)
	p.setFirst(k)
	p.Keys = rest
	return Save(*p)
}

// promote takes the first key out of use: the next key that's on takes
// its place. ok is false when none is.
func promote(p *Provider) (old KeyAccount, ok bool) {
	for i, k := range p.Keys {
		if !k.Off {
			old = p.firstKey()
			p.setFirst(k)
			p.Keys = append(p.Keys[:i:i], p.Keys[i+1:]...)
			return old, true
		}
	}
	return KeyAccount{}, false
}

// SetKeyOn turns a key on or off. The last key that's on stays on.
func SetKeyOn(id, keyRef string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	switch {
	case i >= 0:
		p.Keys[i].Off = !on
	case !on:
		old, ok := promote(p)
		if !ok {
			return errors.New("that's the only key in use; turn another on first")
		}
		old.Off = true
		p.Keys = append([]KeyAccount{old}, p.Keys...)
	default:
		return nil
	}
	return Save(*p)
}

// RemoveKey forgets a key. The last key in use stays: turn another on first.
func RemoveKey(id, keyRef string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i >= 0 {
		p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
	} else if _, ok := promote(p); !ok {
		return errors.New("that's the only key in use; turn another on before removing it")
	}
	return Save(*p)
}

// RenameKey names one of a provider's keys, "Personal", "Team".
func RenameKey(id, keyRef, name string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	name = strings.TrimSpace(name)
	if i < 0 {
		p.KeyName = name
	} else {
		p.Keys[i].Name = name
	}
	return Save(*p)
}
