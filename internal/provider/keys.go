package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// KeyEntry is one API key beyond a provider's first one. The first key lives
// in Provider.Key (with KeyName and KeyProtocol) and is the one in use.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // empty: usable on any endpoint
	Off      bool     `json:"off,omitempty"`      // disabled: never tried
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in hex.
// It is what the CLI and the key functions use to name a key.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// ParseKeyProtocol accepts the protocols a key can be limited to: chat,
// responses, anthropic, or empty for any. Anything else is an error.
func ParseKeyProtocol(s string) (Protocol, error) {
	p := Protocol(strings.ToLower(strings.TrimSpace(s)))
	switch p {
	case "", Chat, Responses, Anthropic:
		return p, nil
	}
	return "", fmt.Errorf("a key's protocol is chat, responses, anthropic, or empty for any; not %q", s)
}

// keyChain is the provider's keys in priority order: the current key first
// (always enabled), then the extras as stored.
func (p Provider) keyChain() []KeyEntry {
	if p.Key == "" {
		return nil
	}
	out := make([]KeyEntry, 0, 1+len(p.Keys))
	out = append(out, KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	return append(out, p.Keys...)
}

// setKeyChain writes a key chain back onto the provider. chain[0] becomes the
// current key and must be enabled.
func (p *Provider) setKeyChain(chain []KeyEntry) {
	first := chain[0]
	p.Key, p.KeyName, p.KeyProtocol = first.Key, first.Name, first.Protocol
	p.Keys = nil
	if len(chain) > 1 {
		p.Keys = append([]KeyEntry(nil), chain[1:]...)
	}
}

// normalizeKeys trims the key fields, drops empty and repeated keys, and
// makes sure the current key is set and enabled.
func normalizeKeys(p Provider) Provider {
	raw := append([]KeyEntry{{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}}, p.Keys...)
	var chain []KeyEntry
	seen := map[string]bool{}
	for _, e := range raw {
		e.Key = strings.TrimSpace(e.Key)
		e.Name = strings.TrimSpace(e.Name)
		e.Protocol = Protocol(strings.ToLower(strings.TrimSpace(string(e.Protocol))))
		if e.Key == "" || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		chain = append(chain, e)
	}
	if len(chain) == 0 {
		p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
		return p
	}
	chain[0].Off = false
	p.setKeyChain(chain)
	return p
}

// checkKeys rejects a provider whose key protocols are not valid.
func checkKeys(p Provider) error {
	if _, err := ParseKeyProtocol(string(p.KeyProtocol)); err != nil {
		return err
	}
	for _, e := range p.Keys {
		if _, err := ParseKeyProtocol(string(e.Protocol)); err != nil {
			return err
		}
	}
	return nil
}

// findKey locates a key in a chain by fingerprint.
func findKey(chain []KeyEntry, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, e := range chain {
		if KeyRef(e.Key) == ref {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no key %q on this provider", ref)
}

// nextEnabled is the index of the first enabled key other than skip, or -1.
func nextEnabled(chain []KeyEntry, skip int) int {
	for i, e := range chain {
		if i != skip && !e.Off {
			return i
		}
	}
	return -1
}

// editKeys applies change to a stored provider's keys and saves the file.
// Signed-in accounts carry no keys of their own.
func editKeys(id string, change func(p *Provider) error) error {
	found, err := Find(id)
	if err != nil {
		return err
	}
	if found.Account != nil {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", found.Name)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != found.ID {
			continue
		}
		p := normalize(f.Providers[i])
		if err := change(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return commit(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. The first key a provider gets becomes its
// current key. Later keys start enabled and queue behind the ones already there.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("an empty key cannot be added")
	}
	proto, err := ParseKeyProtocol(string(proto))
	if err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		for _, e := range chain {
			if e.Key == key {
				return fmt.Errorf("that key is already on %s (%s)", p.Name, KeyRef(key))
			}
		}
		chain = append(chain, KeyEntry{Name: strings.TrimSpace(name), Key: key, Protocol: proto})
		p.setKeyChain(chain)
		return nil
	})
}

// UseKey makes the key the current one. The key it replaces stays enabled and
// moves to second place; names and protocol limits travel with their keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		i, err := findKey(chain, keyRef)
		if err != nil {
			return err
		}
		if i == 0 {
			return nil
		}
		picked := chain[i]
		picked.Off = false
		next := make([]KeyEntry, 0, len(chain))
		next = append(next, picked, chain[0])
		next = append(next, chain[1:i]...)
		next = append(next, chain[i+1:]...)
		p.setKeyChain(next)
		return nil
	})
}

// SetKeyOn enables or disables a key. Disabling the current key hands the job
// to the next enabled one, and the old current key is kept, disabled, in the
// extras. The last enabled key cannot be disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		i, err := findKey(chain, keyRef)
		if err != nil {
			return err
		}
		if on {
			chain[i].Off = false
			p.setKeyChain(chain)
			return nil
		}
		if chain[i].Off {
			return nil
		}
		j := nextEnabled(chain, i)
		if j < 0 {
			return errors.New("the last enabled key cannot be turned off")
		}
		if i > 0 {
			chain[i].Off = true
			p.setKeyChain(chain)
			return nil
		}
		cur := chain[0]
		cur.Off = true
		next := make([]KeyEntry, 0, len(chain))
		next = append(next, chain[j], cur)
		for k := 1; k < len(chain); k++ {
			if k != j {
				next = append(next, chain[k])
			}
		}
		p.setKeyChain(next)
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be removed. Removing
// the current key hands the job to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		i, err := findKey(chain, keyRef)
		if err != nil {
			return err
		}
		if !chain[i].Off && nextEnabled(chain, i) < 0 {
			return errors.New("the last enabled key cannot be removed")
		}
		rest := make([]KeyEntry, 0, len(chain)-1)
		rest = append(rest, chain[:i]...)
		rest = append(rest, chain[i+1:]...)
		if i == 0 {
			j := nextEnabled(rest, -1)
			next := make([]KeyEntry, 0, len(rest))
			next = append(next, rest[j])
			next = append(next, rest[:j]...)
			next = append(next, rest[j+1:]...)
			rest = next
		}
		p.setKeyChain(rest)
		return nil
	})
}

// RenameKey sets a key's name. An empty name clears it.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		i, err := findKey(chain, keyRef)
		if err != nil {
			return err
		}
		chain[i].Name = strings.TrimSpace(name)
		p.setKeyChain(chain)
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	proto, err := ParseKeyProtocol(string(proto))
	if err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		chain := p.keyChain()
		i, err := findKey(chain, keyRef)
		if err != nil {
			return err
		}
		chain[i].Protocol = proto
		p.setKeyChain(chain)
		return nil
	})
}

// KeyInfo describes one key for display. The key itself is masked.
type KeyInfo struct {
	Ref      string
	Name     string
	Masked   string
	Protocol Protocol
	Current  bool
	Off      bool
}

// KeyInfos lists the provider's keys in priority order, current first.
func (p Provider) KeyInfos() []KeyInfo {
	var out []KeyInfo
	for i, e := range p.keyChain() {
		out = append(out, KeyInfo{Ref: KeyRef(e.Key), Name: e.Name, Masked: Mask(e.Key),
			Protocol: e.Protocol, Current: i == 0, Off: e.Off})
	}
	return out
}

// Attempt is one way to send a request to a provider: with one of its keys,
// to one of its endpoints.
type Attempt struct {
	Provider Provider // the provider, carrying this attempt's key as Key
	KeyRef   string   // the key's fingerprint; "" when the provider has no key
	KeyName  string
	To       Protocol // the endpoint the request goes to
	Rank     int      // lower is tried first
	// Limited is set when the key is limited to To. Such a key must never be
	// sent to any other endpoint of the vendor, not even by a retry.
	Limited bool
}

type family int

const (
	famOther family = iota
	famClaude
	famOpenAI
)

var openAIModel = regexp.MustCompile(`^(o[0-9]|gpt|chatgpt)|codex`)

// familyOf sorts a model id into the families that prefer different keys.
func familyOf(model string) family {
	m := model
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = strings.ToLower(m)
	switch {
	case strings.Contains(m, "claude"):
		return famClaude
	case openAIModel.MatchString(m):
		return famOpenAI
	}
	return famOther
}

// matchesFamily reports whether a key limited to proto is the natural pick
// for the model family: Anthropic for Claude, Chat or Responses for OpenAI
// models, and the client's own protocol for everything else.
func matchesFamily(proto, from Protocol, fam family) bool {
	switch fam {
	case famClaude:
		return proto == Anthropic
	case famOpenAI:
		return proto == Chat || proto == Responses
	}
	return proto == from
}

// defaultTo is the endpoint a key with no limit uses: the client's own
// protocol when the vendor speaks it, else the first one it speaks.
func (p Provider) defaultTo(from Protocol) Protocol {
	if p.Base(from) != "" {
		return from
	}
	if s := p.Speaks(); len(s) > 0 {
		return s[0]
	}
	return ""
}

// Attempts lists how a request from the client protocol from, for the model
// (the vendor's own id), may reach the provider, best first. Disabled keys are
// left out, and a limited key appears only if the vendor has its endpoint.
//
// Keys limited to the protocol the model family prefers come first, then keys
// without a limit, then limited keys that need a translation as a last resort.
func (p Provider) Attempts(from Protocol, model string) []Attempt {
	fam := familyOf(model)
	chain := p.keyChain()
	if len(chain) == 0 {
		chain = []KeyEntry{{}} // keyless vendors: one attempt, no key
	}
	var out []Attempt
	for _, e := range chain {
		if e.Off {
			continue
		}
		a := Attempt{Provider: p, KeyName: e.Name}
		if e.Key != "" {
			a.Provider.Key = e.Key
			a.KeyRef = KeyRef(e.Key)
		}
		if e.Protocol != "" {
			if p.Base(e.Protocol) == "" {
				continue
			}
			a.To = e.Protocol
			a.Limited = true
			a.Rank = 2
			if matchesFamily(e.Protocol, from, fam) {
				a.Rank = 0
			}
		} else {
			a.To = p.defaultTo(from)
			a.Rank = 1
			if a.To == "" {
				continue
			}
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}
