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

// ExtraKey is a key a provider holds besides its current one: a second
// account's key, a team key, or one limited to a single protocol. Off keys
// are kept but never tried.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"`
	Off      bool     `json:"off,omitempty"`
}

// current is the keyPos of the provider's current key (the top-level key).
const current = -1

// KeyRef is a key's fingerprint, the name the CLI and the call records use:
// the first five bytes of its SHA-256, in lowercase hex.
func KeyRef(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:5])
}

// ValidKeyProtocol reports whether a key may be limited to proto: "" (any),
// or one of the protocols magpie speaks upstream.
func ValidKeyProtocol(proto Protocol) bool {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

func badProtocol(proto Protocol) error {
	return fmt.Errorf("key protocol %q is not one of chat, responses, anthropic, or any", proto)
}

// isAccount reports whether id is a signed-in agent, which holds no keys.
func isAccount(id string) bool {
	_, ok := find(Accounts(), id)
	return ok
}

// tidyKeys trims the key fields, drops empty keys and any extra key that
// repeats the current one.
func tidyKeys(p *Provider) {
	p.Key = strings.TrimSpace(p.Key)
	var out []ExtraKey
	for _, k := range p.Keys {
		k.Key = strings.TrimSpace(k.Key)
		k.Name = strings.TrimSpace(k.Name)
		if k.Key == "" || (p.Key != "" && k.Key == p.Key) {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o.Key == k.Key
		}
		if !dup {
			out = append(out, k)
		}
	}
	p.Keys = out
}

// KeyInfo describes one key of a provider, for display.
type KeyInfo struct {
	Name     string
	Ref      string
	Mask     string
	Protocol Protocol
	Current  bool // the key requests use first
	On       bool
}

// KeyList lists the provider's keys, the current one first.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{Name: p.KeyName, Ref: KeyRef(p.Key), Mask: Mask(p.Key), Protocol: p.KeyProtocol, Current: true, On: true})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{Name: k.Name, Ref: KeyRef(k.Key), Mask: Mask(k.Key), Protocol: k.Protocol, On: !k.Off})
	}
	return out
}

// enabledCount is how many keys may be tried: the current one, if any, and
// the extra keys that are on.
func (p *Provider) enabledCount() int {
	n := 0
	if p.Key != "" {
		n++
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			n++
		}
	}
	return n
}

// firstEnabled is the index of the first extra key that is on, or -1.
func (p *Provider) firstEnabled() int {
	for i, k := range p.Keys {
		if !k.Off && k.Key != "" {
			return i
		}
	}
	return -1
}

// keyPos finds a key by fingerprint: current, or the index among the extras.
func (p *Provider) keyPos(ref string) (int, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return 0, false
	}
	if p.Key != "" && KeyRef(p.Key) == ref {
		return current, true
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == ref {
			return i, true
		}
	}
	return 0, false
}

// promote makes extra key j the current one. The old current key goes to the
// front of the extras when keepOld is set, switched off when offOld is set.
func (p *Provider) promote(j int, keepOld, offOld bool) {
	next := p.Keys[j]
	rest := make([]ExtraKey, 0, len(p.Keys))
	if keepOld && p.Key != "" {
		rest = append(rest, ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: offOld})
	}
	for i, k := range p.Keys {
		if i != j {
			rest = append(rest, k)
		}
	}
	p.Keys = rest
	p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
}

// editKeys runs fn on the stored entry for a provider that holds keys, then
// saves it.
func editKeys(id string, fn func(p *Provider) error) error {
	found, err := Find(id)
	if err != nil {
		return err
	}
	if found.Account != nil {
		return fmt.Errorf("%s is a signed-in account; its sign-in holds the key", found.Name)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == found.ID {
			q := f.Providers[i]
			if err := fn(&q); err != nil {
				return err
			}
			tidyKeys(&q)
			f.Providers[i] = q
			return store(f)
		}
	}
	return fmt.Errorf("no provider %q", id)
}

func noKey(p *Provider, ref string) error {
	return fmt.Errorf("%s has no key %s; magpie provider key %s lists them", p.Name, ref, p.ID)
}

func needOne(p *Provider) error {
	return fmt.Errorf("%s needs at least one enabled key; add another key first", p.Name)
}

// AddKey adds a key to a provider. A provider with no key takes it as the
// current one; otherwise it is appended, on, after the existing keys.
func AddKey(id, name, key string, proto Protocol) error {
	key, name = strings.TrimSpace(key), strings.TrimSpace(name)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	return editKeys(id, func(p *Provider) error {
		if _, ok := p.keyPos(KeyRef(key)); ok {
			return fmt.Errorf("%s already has that key (%s)", p.Name, KeyRef(key))
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, ExtraKey{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !ValidKeyProtocol(proto) {
		return badProtocol(proto)
	}
	return editKeys(id, func(p *Provider) error {
		pos, ok := p.keyPos(keyRef)
		if !ok {
			return noKey(p, keyRef)
		}
		if pos == current {
			p.KeyProtocol = proto
		} else {
			p.Keys[pos].Protocol = proto
		}
		return nil
	})
}

// UseKey makes a key the current one. The key it replaces stays on, right
// after it, with its name and protocol.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		pos, ok := p.keyPos(keyRef)
		if !ok {
			return noKey(p, keyRef)
		}
		if pos != current {
			p.promote(pos, true, false)
		}
		return nil
	})
}

// SetKeyOn switches a key on or off. Turning off the current key hands the
// job to the next key that is on, and the old one stays, off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		pos, ok := p.keyPos(keyRef)
		if !ok {
			return noKey(p, keyRef)
		}
		if pos == current {
			if on {
				return nil
			}
			j := p.firstEnabled()
			if j < 0 {
				return needOne(p)
			}
			p.promote(j, true, true)
			return nil
		}
		k := &p.Keys[pos]
		if on {
			k.Off = false
			return nil
		}
		if k.Off {
			return nil
		}
		if p.enabledCount() <= 1 {
			return needOne(p)
		}
		k.Off = true
		return nil
	})
}

// RemoveKey deletes a key. The last key that is on cannot go; removing the
// current key hands the job to the next key that is on.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		pos, ok := p.keyPos(keyRef)
		if !ok {
			return noKey(p, keyRef)
		}
		if pos == current {
			j := p.firstEnabled()
			if j < 0 {
				return needOne(p)
			}
			p.promote(j, false, false)
			return nil
		}
		if !p.Keys[pos].Off && p.enabledCount() <= 1 {
			return needOne(p)
		}
		p.Keys = append(p.Keys[:pos], p.Keys[pos+1:]...)
		return nil
	})
}

// RenameKey names a key; "" clears the name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		pos, ok := p.keyPos(keyRef)
		if !ok {
			return noKey(p, keyRef)
		}
		if pos == current {
			p.KeyName = name
		} else {
			p.Keys[pos].Name = name
		}
		return nil
	})
}

// KeyPlan is one key of a provider, ready to send a request with.
type KeyPlan struct {
	Provider  Provider   // the provider, with this key as its key
	Ref       string     // the key's fingerprint; "" when the provider needs none
	Name      string     // the key's name, if it has one
	Protocol  Protocol   // the protocol the key is limited to; "" for any
	Upstreams []Protocol // the endpoints this key may be sent to, preferred first
}

// KeyPlans lists the keys that may serve a request in protocol from for the
// given model, best first. A key limited to a protocol the provider has no
// endpoint for is left out. Claude models prefer Anthropic keys, GPT, o-series
// and codex models prefer OpenAI keys, and other models prefer a key that can
// speak the client's protocol directly, so no translation is needed. Keys that
// fit poorly come last, as the final resort.
func (p Provider) KeyPlans(from Protocol, model string) []KeyPlan {
	if p.Account != nil {
		return p.accountPlans()
	}
	type cand struct {
		key, name string
		proto     Protocol
	}
	var cands []cand
	active := false
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			active = true
		}
	}
	if p.Key != "" || !active {
		cands = append(cands, cand{p.Key, p.KeyName, p.KeyProtocol})
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			cands = append(cands, cand{k.Key, k.Name, k.Protocol})
		}
	}
	fam := familyOf(model)
	type ranked struct {
		plan KeyPlan
		rank int
	}
	var out []ranked
	for _, c := range cands {
		ups := p.upstreamsFor(c.proto)
		if len(ups) == 0 {
			continue
		}
		pl := p
		pl.Key, pl.KeyName, pl.KeyProtocol, pl.Keys = c.key, c.name, c.proto, nil
		ref := ""
		if c.key != "" {
			ref = KeyRef(c.key)
		}
		out = append(out, ranked{KeyPlan{Provider: pl, Ref: ref, Name: c.name, Protocol: c.proto, Upstreams: ups},
			keyRank(fam, c.proto, ups, from)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	plans := make([]KeyPlan, len(out))
	for i, r := range out {
		plans[i] = r.plan
	}
	return plans
}

// upstreamsFor lists the endpoints a key limited to kp may be sent to. A key
// limited to chat or responses may use either OpenAI-style endpoint; one
// limited to anthropic only the Anthropic endpoint; an unlimited key any.
func (p Provider) upstreamsFor(kp Protocol) []Protocol {
	switch kp {
	case "":
		return p.Speaks()
	case Anthropic:
		if p.Anthropic != "" {
			return []Protocol{Anthropic}
		}
	case Chat, Responses:
		var out []Protocol
		for _, pr := range []Protocol{Chat, Responses} {
			if p.Base(pr) != "" {
				out = append(out, pr)
			}
		}
		return out
	}
	return nil
}

// keyRank orders keys for a request: lower is better.
func keyRank(fam string, kp Protocol, ups []Protocol, from Protocol) int {
	switch fam {
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
	if protoIn(ups, from) {
		return 0
	}
	if kp == "" {
		return 1
	}
	return 2
}

var openAISeries = regexp.MustCompile(`^o\d`)

// familyOf names the model family that decides which key suits a model:
// "claude", "openai" (GPT, o-series, codex), or "" for anything else.
func familyOf(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"), openAISeries.MatchString(m):
		return "openai"
	}
	return ""
}

func protoIn(ps []Protocol, p Protocol) bool {
	for _, x := range ps {
		if x == p {
			return true
		}
	}
	return false
}
