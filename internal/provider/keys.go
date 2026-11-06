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

// KeyEntry is an extra API key of a provider, kept beside the key in use
// (Provider.Key). A key may be limited to one protocol and may be switched off.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is the reference the CLI and the call log use for a key: the first
// five bytes of its SHA-256, in lowercase hex. The key itself never appears.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProtocol is true for the protocols a key may be limited to, or for
// no limit at all.
func validKeyProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

// keyLocation finds a key by reference: -1 is the key in use, n >= 0 is
// Keys[n]. ok is false when no key of the provider has that reference.
func keyLocation(p Provider, ref string) (int, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return -1, true
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == ref {
			return i, true
		}
	}
	return 0, false
}

// keyTaken is true when the key is already one of the provider's keys.
func keyTaken(p Provider, key string) bool {
	_, ok := keyLocation(p, KeyRef(key))
	return ok
}

// firstEnabled is the index of the first enabled entry in Keys, or -1.
func firstEnabled(keys []KeyEntry) int {
	for i, k := range keys {
		if !k.Off {
			return i
		}
	}
	return -1
}

// l1Without returns a copy of keys with entry i removed.
func l1Without(keys []KeyEntry, i int) []KeyEntry {
	out := make([]KeyEntry, 0, len(keys))
	out = append(out, keys[:i]...)
	return append(out, keys[i+1:]...)
}

// editKey applies fn to a stored, user-owned provider and saves the result.
// fn works on a copy, so a failing edit leaves the file alone.
func editKey(id string, fn func(p *Provider) error) error {
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		q := f.Providers[i]
		if q.Chat == "" && q.Responses == "" && q.Anthropic == "" {
			break // only model picks, no endpoint: not a provider yet
		}
		q.Keys = append([]KeyEntry(nil), q.Keys...)
		if err := fn(&q); err != nil {
			return err
		}
		f.Providers[i] = q
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. The first key a provider gets is the one
// in use; later keys are enabled and queue behind the ones before them.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("a key cannot be empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", proto)
	}
	name = strings.TrimSpace(name)
	return editKey(id, func(p *Provider) error {
		if keyTaken(*p, key) {
			return fmt.Errorf("%s already has that key", p.ID)
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, KeyEntry{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// UseKey makes a key the one in use. The key it replaces stays enabled and
// moves to the front of the queue, keeping its name and protocol limit.
func UseKey(id, keyRef string) error {
	return editKey(id, func(p *Provider) error {
		i, ok := keyLocation(*p, keyRef)
		if !ok {
			return fmt.Errorf("%s has no key %q", p.ID, keyRef)
		}
		if i == -1 {
			return nil
		}
		next := p.Keys[i]
		if next.Off {
			return fmt.Errorf("key is disabled; enable it first")
		}
		prev := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		p.Keys = append([]KeyEntry{prev}, l1Without(p.Keys, i)...)
		return nil
	})
}

// SetKeyOn enables or disables a key. The key in use is always enabled, so
// turning it off hands the job to the next enabled key, which leaves the
// queue; the key turned off goes to the front of the queue, kept but off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKey(id, func(p *Provider) error {
		i, ok := keyLocation(*p, keyRef)
		if !ok {
			return fmt.Errorf("%s has no key %q", p.ID, keyRef)
		}
		if i >= 0 {
			p.Keys[i].Off = !on
			return nil
		}
		if on {
			return nil
		}
		next := firstEnabled(p.Keys)
		if next < 0 {
			return fmt.Errorf("%s has no other enabled key to take over; enable another key first", p.ID)
		}
		take := p.Keys[next]
		prev := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}
		p.Keys = append([]KeyEntry{prev}, l1Without(p.Keys, next)...)
		p.Key, p.KeyName, p.KeyProtocol = take.Key, take.Name, take.Protocol
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot go; removing the key
// in use hands the job to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return editKey(id, func(p *Provider) error {
		i, ok := keyLocation(*p, keyRef)
		if !ok {
			return fmt.Errorf("%s has no key %q", p.ID, keyRef)
		}
		if i >= 0 {
			p.Keys = l1Without(p.Keys, i)
			return nil
		}
		next := firstEnabled(p.Keys)
		if next < 0 {
			return fmt.Errorf("%s would have no enabled key left; add or enable another key first", p.ID)
		}
		take := p.Keys[next]
		p.Key, p.KeyName, p.KeyProtocol = take.Key, take.Name, take.Protocol
		p.Keys = l1Without(p.Keys, next)
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKey(id, func(p *Provider) error {
		i, ok := keyLocation(*p, keyRef)
		if !ok {
			return fmt.Errorf("%s has no key %q", p.ID, keyRef)
		}
		if i == -1 {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProtocol(proto) {
		return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", proto)
	}
	return editKey(id, func(p *Provider) error {
		i, ok := keyLocation(*p, keyRef)
		if !ok {
			return fmt.Errorf("%s has no key %q", p.ID, keyRef)
		}
		if i == -1 {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}

// slotProvider returns a copy of p limited to one protocol's endpoint.
func slotProvider(p Provider, proto Protocol) Provider {
	if proto == "" {
		return p
	}
	if proto != Chat {
		p.Chat = ""
	}
	if proto != Responses {
		p.Responses = ""
	}
	if proto != Anthropic {
		p.Anthropic = ""
	}
	return p
}

// keySlots lists the enabled keys in the order they queue, each as a copy of
// the provider carrying that key. A key limited to one protocol keeps only
// that endpoint; a key whose endpoint the provider lacks is left out.
func (p Provider) keySlots() []Provider {
	if p.Account != nil {
		return p.accountSlots()
	}
	if len(p.Keys) == 0 && p.KeyProtocol == "" {
		return []Provider{p}
	}
	var out []Provider
	add := func(name, key string, proto Protocol) {
		c := slotProvider(p, proto)
		c.Key, c.KeyName, c.KeyProtocol = key, name, proto
		c.Keys = nil
		if len(c.Speaks()) == 0 {
			return
		}
		out = append(out, c)
	}
	add(p.KeyName, p.Key, p.KeyProtocol)
	for _, k := range p.Keys {
		if !k.Off {
			add(k.Name, k.Key, k.Protocol)
		}
	}
	return out
}

// modelFamily groups model ids the way key choice cares about.
type modelFamily int

const (
	familyOther modelFamily = iota
	familyClaude
	familyOpenAI
)

var openAIModel = regexp.MustCompile(`^(gpt|chatgpt|o\d)`)

func familyOf(model string) modelFamily {
	base := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		base = model[i+1:]
	}
	base = strings.ToLower(base)
	switch {
	case strings.Contains(base, "claude"):
		return familyClaude
	case openAIModel.MatchString(base), strings.Contains(base, "codex"):
		return familyOpenAI
	}
	return familyOther
}

// l1Rank orders keys for a model: lower is tried first. Claude models prefer
// the Anthropic key, GPT / o-series / codex models the OpenAI-style keys;
// other models prefer a key that speaks the client's protocol directly, so
// no translation is needed. Keys that fit poorly are last resorts.
func l1Rank(c Provider, from Protocol, model string) int {
	switch familyOf(model) {
	case familyClaude:
		switch c.KeyProtocol {
		case Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case familyOpenAI:
		switch c.KeyProtocol {
		case Chat, Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if c.Base(from) != "" {
		return 0
	}
	return 1
}

// Candidates lists the keys to try for one request, best first. from is the
// protocol the client speaks and model the vendor's model id. Each candidate
// is a provider copy carrying just that key (see keySlots).
func (p Provider) Candidates(from Protocol, model string) []Provider {
	slots := p.keySlots()
	sort.SliceStable(slots, func(i, j int) bool {
		return l1Rank(slots[i], from, model) < l1Rank(slots[j], from, model)
	})
	return slots
}
