package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// KeyEntry is an extra API key a provider holds besides its current one.
// Off keys are kept but never tried.
type KeyEntry struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" is any protocol
	Off      bool     `json:"off,omitempty"`
}

// KeyRef is a key's fingerprint, the first five bytes of its SHA-256 as ten
// lowercase hex digits. It names a key without showing it.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// isKeyProtocol is true for the protocols a key may be limited to.
func isKeyProtocol(p Protocol) bool {
	return p == Chat || p == Responses || p == Anthropic
}

// validKeyProtocol accepts a key protocol, or "" for any.
func validKeyProtocol(p Protocol) bool {
	return p == "" || isKeyProtocol(p)
}

// checkKeyProtocols rejects a stored key protocol that is not one of the known values.
func checkKeyProtocols(p Provider) error {
	if !validKeyProtocol(p.KeyProtocol) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic or nothing", p.KeyProtocol)
	}
	for _, k := range p.Keys {
		if !validKeyProtocol(k.Protocol) {
			return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic or nothing", k.Protocol)
		}
	}
	return nil
}

// cleanKeys trims the extra keys and drops empty ones. An empty result is nil.
func cleanKeys(ks []KeyEntry) []KeyEntry {
	var out []KeyEntry
	for _, k := range ks {
		k.Name = strings.TrimSpace(k.Name)
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// keyUse is one enabled key as a request sees it.
type keyUse struct {
	name  string
	key   string
	proto Protocol
}

// enabledKeys lists the keys a request may use: the current key first, then
// the enabled extras in stored order.
func (p Provider) enabledKeys() []keyUse {
	var out []keyUse
	if p.Key != "" {
		out = append(out, keyUse{p.KeyName, p.Key, p.KeyProtocol})
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			out = append(out, keyUse{k.Name, k.Key, k.Protocol})
		}
	}
	return out
}

// enabledCount counts the enabled keys, the current one included.
func (p *Provider) enabledCount() int {
	return len(p.enabledKeys())
}

// firstEnabled is the index of the first enabled extra key, or -1.
func firstEnabled(ks []KeyEntry) int {
	for i, k := range ks {
		if !k.Off && k.Key != "" {
			return i
		}
	}
	return -1
}

// without returns a copy of ks with element i removed.
func without(ks []KeyEntry, i int) []KeyEntry {
	out := make([]KeyEntry, 0, len(ks))
	out = append(out, ks[:i]...)
	return append(out, ks[i+1:]...)
}

// locate finds the key a fingerprint names: -1 for the current key, else an
// index into Keys.
func (p *Provider) locate(ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return -1, nil
	}
	for i, k := range p.Keys {
		if k.Key != "" && KeyRef(k.Key) == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s has no key %q", p.ID, ref)
}

// editKeys loads the stored record of a provider, applies fn to a copy, and
// stores the copy only when fn succeeds.
func editKeys(id string, fn func(p *Provider) error) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := f.Providers[i]
		p.Keys = append([]KeyEntry(nil), p.Keys...)
		if err := fn(&p); err != nil {
			return err
		}
		f.Providers[i] = p
		return store(f)
	}
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it takes no keys", id)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds an API key to a provider. The first key becomes the current
// one; later keys are enabled and go after the existing ones.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic or nothing", proto)
	}
	return editKeys(id, func(p *Provider) error {
		if p.Key == key || containsKey(p.Keys, key) {
			return fmt.Errorf("that key is already on %s", p.ID)
		}
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, name, proto
			return nil
		}
		p.Keys = append(p.Keys, KeyEntry{Name: name, Key: key, Protocol: proto})
		return nil
	})
}

func containsKey(ks []KeyEntry, key string) bool {
	for _, k := range ks {
		if k.Key == key {
			return true
		}
	}
	return false
}

// UseKey makes a key the current one. The key it replaces stays enabled and
// goes right after it; names and protocol limits travel with their keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i == -1 {
			return nil
		}
		next := p.Keys[i]
		old := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
		keys := append([]KeyEntry{old}, without(p.Keys, i)...)
		p.Keys = keys
		p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		return nil
	})
}

// SetKeyOn enables or disables a key. Turning off the current key hands the
// current place to the next enabled key and keeps the old one, disabled. The
// last enabled key cannot be turned off.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i == -1 {
			if on {
				return nil
			}
			j := firstEnabled(p.Keys)
			if j < 0 {
				return fmt.Errorf("%s has no other enabled key to take over", p.ID)
			}
			next := p.Keys[j]
			old := KeyEntry{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Off: true}
			p.Keys = append([]KeyEntry{old}, without(p.Keys, j)...)
			p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
			return nil
		}
		if !on && !p.Keys[i].Off && p.enabledCount() <= 1 {
			return errors.New("the last enabled key can't be turned off")
		}
		p.Keys[i].Off = !on
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be deleted, and
// deleting the current key hands the place to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		enabled := i == -1 || !p.Keys[i].Off
		if enabled && p.enabledCount() <= 1 {
			return errors.New("the last enabled key can't be deleted")
		}
		if i >= 0 {
			p.Keys = without(p.Keys, i)
		} else {
			j := firstEnabled(p.Keys)
			next := p.Keys[j]
			p.Keys = without(p.Keys, j)
			p.Key, p.KeyName, p.KeyProtocol = next.Key, next.Name, next.Protocol
		}
		if len(p.Keys) == 0 {
			p.Keys = nil
		}
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i == -1 {
			p.KeyName = name
		} else {
			p.Keys[i].Name = name
		}
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or clears the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic or nothing", proto)
	}
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i == -1 {
			p.KeyProtocol = proto
		} else {
			p.Keys[i].Protocol = proto
		}
		return nil
	})
}

// Choice is one key a request may be sent with. Provider is the provider as
// that key sees it: a key limited to one protocol keeps only that endpoint.
type Choice struct {
	Provider Provider
	Ref      string // the key's fingerprint; "" when the provider has no key
	Name     string // the key's name; "" when unnamed
}

// Choices lists the keys a request for model, arriving in protocol from, may
// use, most suitable first. Keys limited to a protocol the provider has no
// endpoint for are left out. A provider with no key settings yields itself.
func (p Provider) Choices(from Protocol, model string) []Choice {
	if p.Account != nil {
		// a signed-in agent serves through each of its logins that is on: the
		// current one first, each with its own credentials
		var out []Choice
		for _, q := range p.servingLogins() {
			out = append(out, Choice{Provider: q, Ref: KeyRef("login:" + strings.ToLower(q.Account.User)), Name: q.Account.User})
		}
		return out
	}
	if len(p.Keys) == 0 && p.KeyProtocol == "" {
		c := Choice{Provider: p, Name: p.KeyName}
		if p.Key != "" {
			c.Ref = KeyRef(p.Key)
		}
		return []Choice{c}
	}
	type ranked struct {
		c    Choice
		rank int
	}
	var rs []ranked
	for _, k := range p.enabledKeys() {
		if !validKeyProtocol(k.proto) || (k.proto != "" && p.Base(k.proto) == "") {
			continue
		}
		rs = append(rs, ranked{Choice{Provider: viewFor(p, k), Ref: KeyRef(k.key), Name: k.name}, rankOf(p, k.proto, from, model)})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]Choice, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.c)
	}
	return out
}

// viewFor is the provider as one key sees it: the key set, and for a limited
// key only the endpoint of its protocol.
func viewFor(p Provider, k keyUse) Provider {
	q := p
	q.Key, q.KeyName, q.KeyProtocol, q.Keys = k.key, k.name, k.proto, nil
	if k.proto != "" {
		keep := p.Base(k.proto)
		q.Chat, q.Responses, q.Anthropic = "", "", ""
		switch k.proto {
		case Chat:
			q.Chat = keep
		case Responses:
			q.Responses = keep
		case Anthropic:
			q.Anthropic = keep
		}
	}
	return q
}

// rankOf orders keys for a model: lower ranks are tried first.
//   - Claude models: Anthropic keys, then keys for any protocol, then OpenAI-only keys.
//   - GPT, o-series and codex models: OpenAI keys, then any, then Anthropic-only.
//   - Other models: keys that speak the client's protocol directly, then any key
//     the provider can reach directly, then keys that need translation.
func rankOf(p Provider, proto, from Protocol, model string) int {
	switch {
	case isClaudeModel(model):
		switch proto {
		case Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case isOpenAIModel(model):
		switch proto {
		case Chat, Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if proto != "" {
		if proto == from {
			return 0
		}
		return 3
	}
	if p.Base(from) != "" {
		return 1
	}
	return 2
}

// modelTail is the model id after any "vendor/" prefix, lowercased.
func modelTail(model string) string {
	return strings.ToLower(catalog.Tail(strings.TrimSpace(model)))
}

func isClaudeModel(model string) bool {
	return strings.HasPrefix(modelTail(model), "claude")
}

var oSeries = regexp.MustCompile(`^o[0-9]`)

func isOpenAIModel(model string) bool {
	t := modelTail(model)
	return strings.HasPrefix(t, "gpt") || strings.HasPrefix(t, "chatgpt") ||
		strings.Contains(t, "codex") || oSeries.MatchString(t)
}
