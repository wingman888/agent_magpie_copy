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

// KeyEntry is an extra API key kept beside a provider's current one. Off
// keys are kept but never tried. A key with a protocol only ever reaches
// that protocol's endpoint.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is a key's fingerprint, the way the user names it: the first five
// bytes of SHA-256(key) in lowercase hex.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// KeyChoice is one key a request may be sent with, and the protocol it is
// limited to ("" when it is not limited).
type KeyChoice struct {
	Key      string
	Name     string
	Ref      string
	Protocol Protocol
}

// validKeyProtocol accepts "" (any protocol) and the three a key may be
// limited to. Gemini is only ever served to clients, so it is refused.
func validKeyProtocol(proto Protocol) error {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return nil
	}
	return fmt.Errorf("a key's protocol must be chat, responses or anthropic, not %q", proto)
}

// keyTaken reports whether key is already one of the provider's keys.
func keyTaken(p *Provider, key string) bool {
	if p.Key == key {
		return true
	}
	for _, k := range p.Keys {
		if k.Key == key {
			return true
		}
	}
	return false
}

// locate finds the key a fingerprint names: -1 is the current key, else an
// index into p.Keys.
func locate(p *Provider, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return -1, nil
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s has no key %q; magpie provider %s lists them", p.Name, ref, p.ID)
}

// nextEnabled is the index of the first key that is on, or -1.
func nextEnabled(keys []KeyEntry) int {
	for i, k := range keys {
		if !k.Off {
			return i
		}
	}
	return -1
}

// removeAt returns keys without the entry at i, in a fresh slice.
func removeAt(keys []KeyEntry, i int) []KeyEntry {
	out := make([]KeyEntry, 0, len(keys)-1)
	out = append(out, keys[:i]...)
	return append(out, keys[i+1:]...)
}

// promoteKey makes sure a provider with extra keys has a current one: the
// first key that is on, else the first key at all.
func promoteKey(p *Provider) {
	if p.Key != "" || len(p.Keys) == 0 {
		return
	}
	j := nextEnabled(p.Keys)
	if j < 0 {
		j = 0
	}
	next := p.Keys[j]
	p.Keys = removeAt(p.Keys, j)
	p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
}

// carryKeys keeps what the stored provider had when a save does not say:
// the extra keys, and the current key's name and protocol while the current
// key itself is unchanged.
func carryKeys(old Provider, p *Provider) {
	if p.Keys == nil {
		p.Keys = old.Keys
	}
	if p.Key == old.Key {
		if p.KeyName == "" {
			p.KeyName = old.KeyName
		}
		if p.KeyProtocol == "" {
			p.KeyProtocol = old.KeyProtocol
		}
	}
}

// checkKeys validates and tidies keys before they are stored: names and
// keys are trimmed, empty keys and repeats go, and an unknown protocol is an
// error.
func checkKeys(p *Provider) error {
	if err := validKeyProtocol(p.KeyProtocol); err != nil {
		return err
	}
	seen := map[string]bool{p.Key: true}
	var out []KeyEntry
	for _, k := range p.Keys {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		if err := validKeyProtocol(k.Protocol); err != nil {
			return err
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	p.Keys = out
	return nil
}

// editKeys loads a stored provider, applies change to it and saves it. A
// signed-in account has no keys, so it is refused.
func editKeys(id string, change func(p *Provider) error) error {
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is signed in through its agent and has no keys to change", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		promoteKey(&p)
		if err := change(&p); err != nil {
			return err
		}
		promoteKey(&p)
		f.Providers[i] = p
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. A new key is on and goes after the
// existing ones; a provider with no key gets this one as its current key.
// A repeated key is an error.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("a key needs a value")
	}
	if err := validKeyProtocol(proto); err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		if keyTaken(p, key) {
			return fmt.Errorf("%s already has that key", p.Name)
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, strings.TrimSpace(name), proto
			return nil
		}
		p.Keys = append(p.Keys, KeyEntry{Name: strings.TrimSpace(name), Key: key, Protocol: proto})
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol's endpoint, or lifts the limit
// with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if err := validKeyProtocol(proto); err != nil {
		return err
	}
	return editKeys(id, func(p *Provider) error {
		i, err := locate(p, keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays on, first
// among the others, with its name and protocol.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := locate(p, keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			return nil
		}
		next := p.Keys[i]
		rest := removeAt(p.Keys, i)
		cur := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = append([]KeyEntry{cur}, rest...)
		return nil
	})
}

// SetKeyOn turns a key on or off. Turning off the current key hands the
// role to the next key that is on, and the old one stays as an off key. The
// last key that is on cannot be turned off, and then nothing changes.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		i, err := locate(p, keyRef)
		if err != nil {
			return err
		}
		if on {
			if i >= 0 {
				p.Keys[i].Off = false
			}
			return nil
		}
		if i >= 0 {
			p.Keys[i].Off = true
			return nil
		}
		j := nextEnabled(p.Keys)
		if j < 0 {
			return fmt.Errorf("%s has no other key on; the last one on cannot be turned off", p.Name)
		}
		next := p.Keys[j]
		rest := removeAt(p.Keys, j)
		cur := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = append([]KeyEntry{cur}, rest...)
		return nil
	})
}

// RemoveKey deletes a key. The last key that is on cannot be removed.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := locate(p, keyRef)
		if err != nil {
			return err
		}
		if i >= 0 {
			p.Keys = removeAt(p.Keys, i)
			return nil
		}
		j := nextEnabled(p.Keys)
		if j < 0 {
			return fmt.Errorf("%s has no other key on; the last one on cannot be removed", p.Name)
		}
		next := p.Keys[j]
		p.Keys = removeAt(p.Keys, j)
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		return nil
	})
}

// RenameKey names a key. An empty name leaves it showing only its fingerprint.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		i, err := locate(p, keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// setBase sets one protocol's endpoint.
func (p *Provider) setBase(proto Protocol, url string) {
	switch proto {
	case Chat:
		p.Chat = url
	case Responses:
		p.Responses = url
	case Anthropic:
		p.Anthropic = url
	}
}

// KeyChoices lists the keys a request may be sent with, best first. Off keys
// are left out, and so are keys limited to a protocol the provider has no
// endpoint for. A keyless provider has exactly one choice, with no key.
//
// Claude models prefer an Anthropic key, GPT, o-series and codex models an
// OpenAI key. Other models prefer a key that speaks the client's protocol,
// so nothing needs translating. Keys that do not fit come last.
func (p Provider) KeyChoices(model string, from Protocol) []KeyChoice {
	if p.Key == "" && len(p.Keys) == 0 {
		return []KeyChoice{{}}
	}
	var out []KeyChoice
	add := func(key, name string, proto Protocol) {
		if key == "" || validKeyProtocol(proto) != nil {
			return
		}
		if proto != "" && p.Base(proto) == "" {
			return
		}
		out = append(out, KeyChoice{Key: key, Name: name, Ref: KeyRef(key), Protocol: proto})
	}
	add(p.Key, p.KeyName, p.KeyProtocol)
	for _, k := range p.Keys {
		if !k.Off {
			add(k.Key, k.Name, k.Protocol)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].score(p, model, from) < out[j].score(p, model, from)
	})
	return out
}

// Using is the provider as one key choice sees it: that key in place of the
// current one, and, for a key limited to one protocol, only that protocol's
// endpoint.
func (p Provider) Using(c KeyChoice) Provider {
	p.Key, p.KeyName, p.KeyProtocol = c.Key, c.Name, c.Protocol
	if c.Protocol != "" {
		for _, pr := range Protocols {
			if pr != c.Protocol {
				p.setBase(pr, "")
			}
		}
	}
	return p
}

// score ranks a choice for a model: lower is tried first.
func (c KeyChoice) score(p Provider, model string, from Protocol) int {
	restricted := c.Protocol != ""
	switch modelFamily(model) {
	case "claude":
		switch {
		case c.Protocol == Anthropic:
			return 0
		case !restricted:
			return 1
		}
		return 2
	case "openai":
		switch {
		case c.Protocol == Chat || c.Protocol == Responses:
			return 0
		case !restricted:
			return 1
		}
		return 2
	}
	switch {
	case restricted && c.Protocol == from, !restricted && p.Base(from) != "":
		return 0
	case !restricted:
		return 1
	}
	return 2
}

var oSeriesModel = regexp.MustCompile(`^o\d`)

// modelFamily tells the vendor line of a model from its name: "claude",
// "openai" (GPT, o-series, codex) or "" for anything else.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"), oSeriesModel.MatchString(m):
		return "openai"
	}
	return ""
}
