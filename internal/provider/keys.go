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

// KeyEntry is a key besides the provider's current one. Name is what the user
// calls it; Protocol limits the key to one endpoint (empty means any); Off
// keeps the key saved but out of rotation.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256 in hex.
// The key functions take it to name a key, and it is what the call log shows,
// so the key itself never has to be printed.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProtocol accepts the protocols a key may be limited to; empty means
// the key is not limited.
func validKeyProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

// keyList is every key the provider holds, the current one first.
func (p Provider) keyList() []KeyEntry {
	var out []KeyEntry
	if p.Key != "" {
		out = append(out, KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeyList writes a key list back. The first entry becomes the current key,
// which is always enabled; the rest go to Keys in order.
func (p *Provider) setKeyList(ks []KeyEntry) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(ks) == 0 {
		return
	}
	p.Key, p.KeyName, p.KeyProtocol = ks[0].Key, ks[0].Name, ks[0].Protocol
	if len(ks) > 1 {
		p.Keys = ks[1:]
	}
}

// normalizeKeys tidies the key list: trims it, drops empty and repeated keys,
// and makes sure the current key is set and enabled. When the current key is
// missing, the first enabled extra key takes its place.
func normalizeKeys(p *Provider) {
	var ks []KeyEntry
	seen := map[string]bool{}
	all := append([]KeyEntry{{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}}, p.Keys...)
	for _, e := range all {
		e.Key = strings.TrimSpace(e.Key)
		e.Name = strings.TrimSpace(e.Name)
		if e.Key == "" || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		ks = append(ks, e)
	}
	if len(ks) > 0 {
		j := 0
		for i, e := range ks {
			if !e.Off {
				j = i
				break
			}
		}
		first := ks[j]
		first.Off = false
		rest := append(append([]KeyEntry{}, ks[:j]...), ks[j+1:]...)
		ks = append([]KeyEntry{first}, rest...)
	}
	p.setKeyList(ks)
}

// Serves reports whether a request in protocol proto can use this key: the
// vendor has that endpoint, and the key is not limited to another protocol.
func (p Provider) Serves(proto Protocol) bool {
	return p.Base(proto) != "" && (p.KeyProtocol == "" || p.KeyProtocol == proto)
}

// Endpoints lists the protocols the key may be sent in, preferred first.
func (p Provider) Endpoints() []Protocol {
	var out []Protocol
	for _, pr := range p.Speaks() {
		if p.Serves(pr) {
			out = append(out, pr)
		}
	}
	return out
}

type family int

const (
	famOther family = iota
	famClaude
	famOpenAI
)

var openAIModel = regexp.MustCompile(`^(gpt|chatgpt|o[0-9]|.*codex)`)

// familyOf tells which vendor family a model belongs to, from its name. A
// "vendor/" prefix is ignored.
func familyOf(model string) family {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return famClaude
	case openAIModel.MatchString(m):
		return famOpenAI
	}
	return famOther
}

// rankOf orders keys for a model family; lower goes first. Keys that fit the
// family come first, keys limited to another protocol last.
func rankOf(fam family, keyProto, from Protocol) int {
	switch fam {
	case famClaude:
		switch keyProto {
		case Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case famOpenAI:
		switch keyProto {
		case Chat, Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if keyProto == "" || keyProto == from {
		return 0
	}
	return 1
}

// Candidates lists the ways to send a request in protocol from for model: one
// copy of p per enabled key the vendor can serve, each carrying just that key.
// The slice is in the order to try them. A provider with no keys (a local
// server, or a signed-in agent) gives just itself.
func Candidates(p Provider, from Protocol, model string) []Provider {
	ks := p.keyList()
	if p.Account != nil || len(ks) == 0 {
		return []Provider{p}
	}
	fam := familyOf(model)
	type cand struct {
		p          Provider
		rank, idle int
	}
	var cs []cand
	for _, e := range ks {
		if e.Off || !validKeyProtocol(e.Protocol) {
			continue
		}
		c := p
		c.Keys = nil
		c.Key, c.KeyName, c.KeyProtocol = e.Key, e.Name, e.Protocol
		if len(c.Endpoints()) == 0 {
			continue
		}
		idle := 0
		if !c.Serves(from) {
			idle = 1 // needs translating; a key that speaks the client's protocol goes first
		}
		cs = append(cs, cand{c, rankOf(fam, e.Protocol, from), idle})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].rank != cs[j].rank {
			return cs[i].rank < cs[j].rank
		}
		return cs[i].idle < cs[j].idle
	})
	out := make([]Provider, len(cs))
	for i, c := range cs {
		out[i] = c.p
	}
	return out
}

// editKeys loads provider id, applies change to its key list, and saves it.
// Only the keys are rewritten; the rest of the stored entry is left alone.
func editKeys(id string, change func(ks []KeyEntry) ([]KeyEntry, error)) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account, which has no API keys", id)
	}
	f := load()
	for i := range f.Providers {
		sp := &f.Providers[i]
		if sp.ID != id || (sp.Chat == "" && sp.Responses == "" && sp.Anthropic == "") {
			continue
		}
		normalizeKeys(sp)
		ks, err := change(sp.keyList())
		if err != nil {
			return err
		}
		sp.setKeyList(ks)
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// findKey returns the position of the key with the given fingerprint.
func findKey(ks []KeyEntry, keyRef string) (int, error) {
	for i, e := range ks {
		if KeyRef(e.Key) == keyRef {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no key %q", keyRef)
}

// AddKey adds a key to provider id. The new key is enabled and goes after the
// keys already there; a provider with no key gets this one as its current key.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", proto)
	}
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		for _, e := range ks {
			if e.Key == key {
				return nil, errors.New("that key is already on this provider")
			}
		}
		return append(ks, KeyEntry{Name: strings.TrimSpace(name), Key: key, Protocol: proto}), nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit when proto
// is empty.
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProtocol(proto) {
		return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", proto)
	}
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		i, err := findKey(ks, keyRef)
		if err != nil {
			return nil, err
		}
		ks[i].Protocol = proto
		return ks, nil
	})
}

// UseKey makes a key the current one. The key that was current stays enabled
// and moves right behind it; names and protocol limits travel with their keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		i, err := findKey(ks, keyRef)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			return ks, nil
		}
		chosen := ks[i]
		chosen.Off = false
		out := []KeyEntry{chosen, ks[0]}
		out = append(out, ks[1:i]...)
		out = append(out, ks[i+1:]...)
		return out, nil
	})
}

// SetKeyOn enables or disables a key. Only one enabled key is needed to keep
// a provider working, but the last enabled one cannot be switched off.
// Switching off the current key hands the job to the next enabled key, and
// the old current key stays saved, disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		i, err := findKey(ks, keyRef)
		if err != nil {
			return nil, err
		}
		if on {
			ks[i].Off = false
			return ks, nil
		}
		if i == 0 {
			j := -1
			for k := 1; k < len(ks); k++ {
				if !ks[k].Off {
					j = k
					break
				}
			}
			if j < 0 {
				return nil, errors.New("that is the last enabled key; it cannot be turned off")
			}
			next := ks[j]
			next.Off = false
			cur := ks[0]
			cur.Off = true
			out := []KeyEntry{next, cur}
			out = append(out, ks[1:j]...)
			out = append(out, ks[j+1:]...)
			return out, nil
		}
		// the current key is always enabled, so another enabled key remains
		ks[i].Off = true
		return ks, nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be deleted; removing
// the current key hands the job to the next enabled key.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		i, err := findKey(ks, keyRef)
		if err != nil {
			return nil, err
		}
		enabled := 0
		for _, e := range ks {
			if !e.Off {
				enabled++
			}
		}
		if !ks[i].Off && enabled == 1 {
			return nil, errors.New("that is the last enabled key; it cannot be deleted")
		}
		if i == 0 {
			j := -1
			for k := 1; k < len(ks); k++ {
				if !ks[k].Off {
					j = k
					break
				}
			}
			next := ks[j]
			next.Off = false
			out := []KeyEntry{next}
			out = append(out, ks[1:j]...)
			out = append(out, ks[j+1:]...)
			return out, nil
		}
		out := append([]KeyEntry{}, ks[:i]...)
		return append(out, ks[i+1:]...), nil
	})
}

// RenameKey changes a key's name. An empty name clears it.
func RenameKey(id, keyRef, name string) error {
	return editKeys(id, func(ks []KeyEntry) ([]KeyEntry, error) {
		i, err := findKey(ks, keyRef)
		if err != nil {
			return nil, err
		}
		ks[i].Name = strings.TrimSpace(name)
		return ks, nil
	})
}
