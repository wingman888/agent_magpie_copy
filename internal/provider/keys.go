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

// ExtraKey is a key of a provider besides the current one (Provider.Key).
// A provider may hold several; the gateway tries the enabled ones in order.
type ExtraKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" means the key may serve any endpoint
	Off      bool     `json:"off,omitempty"`      // disabled keys are never tried
}

// KeyRef is the fingerprint the key-management functions take in place of
// the key itself: the first five bytes of its SHA-256, in lowercase hex.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validProtocol is true for the protocols a key may be limited to.
func validProtocol(p Protocol) bool {
	return p == "" || p == Chat || p == Responses || p == Anthropic
}

// checkKeyProtocols rejects a key limited to a protocol magpie cannot name.
func checkKeyProtocols(p Provider) error {
	if !validProtocol(p.KeyProtocol) {
		return fmt.Errorf("unknown key protocol %q; use chat, responses, anthropic or nothing", p.KeyProtocol)
	}
	for _, k := range p.Keys {
		if !validProtocol(k.Protocol) {
			return fmt.Errorf("unknown key protocol %q; use chat, responses, anthropic or nothing", k.Protocol)
		}
	}
	return nil
}

// cleanKeys trims the extra keys, drops empty ones and any that repeat the
// current key or an earlier one.
func cleanKeys(current string, keys []ExtraKey) []ExtraKey {
	var out []ExtraKey
	seen := map[string]bool{}
	if current != "" {
		seen[current] = true
	}
	for _, k := range keys {
		k.Name = strings.TrimSpace(k.Name)
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		out = append(out, k)
	}
	return out
}

// KeyChoice is one enabled key of a provider, as the gateway may try it.
type KeyChoice struct {
	Ref      string   // fingerprint, see KeyRef
	Name     string   // "" when the key has no name
	Key      string   // the key itself
	Protocol Protocol // "" when the key may serve any endpoint
}

// KeyChoices lists the enabled keys: the current one first, then the
// extra keys in order.
func (p Provider) KeyChoices() []KeyChoice {
	var out []KeyChoice
	if p.Key != "" {
		out = append(out, KeyChoice{Ref: KeyRef(p.Key), Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" {
			out = append(out, KeyChoice{Ref: KeyRef(k.Key), Name: k.Name, Key: k.Key, Protocol: k.Protocol})
		}
	}
	return out
}

// Routed is true when the provider's keys need the multi-key path: it has
// extra keys, or its current key is limited to one protocol.
func (p Provider) Routed() bool {
	return len(p.Keys) > 0 || p.KeyProtocol != ""
}

// Attempt is one way to send a request to a provider: a key, the endpoint
// it is used on, and a view of the provider that carries only that key and
// the endpoints it may use.
type Attempt struct {
	Key  KeyChoice
	To   Protocol
	View Provider
}

// Attempts orders the enabled keys for a request arriving in protocol from
// for model. A key limited to a protocol only reaches that protocol's
// endpoint, and a key whose endpoint the provider lacks is left out. With
// several keys the ones fitting the model's family come first; the rest
// follow as a last resort. A single key keeps the usual endpoint choice.
func (p Provider) Attempts(from Protocol, model string) []Attempt {
	keys := p.KeyChoices()
	multi := len(keys) > 1
	pref := endpointPreference(from, model)
	type ranked struct {
		a    Attempt
		rank int
	}
	var rs []ranked
	for _, k := range keys {
		view := p.withKey(k)
		speaks := view.Speaks()
		if len(speaks) == 0 {
			continue
		}
		to, rank := speaks[0], 0
		if view.Base(from) != "" {
			to = from
		}
		if multi {
			to, rank = pickEndpoint(view, from, pref)
		}
		rs = append(rs, ranked{Attempt{Key: k, To: to, View: view}, rank})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]Attempt, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.a)
	}
	return out
}

// withKey is the provider as seen through one key: that key, and only the
// endpoints the key may use.
func (p Provider) withKey(k KeyChoice) Provider {
	v := p
	v.Key, v.KeyName, v.KeyProtocol, v.Keys = k.Key, "", "", nil
	if k.Protocol != "" {
		if k.Protocol != Chat {
			v.Chat = ""
		}
		if k.Protocol != Responses {
			v.Responses = ""
		}
		if k.Protocol != Anthropic {
			v.Anthropic = ""
		}
	}
	return v
}

var openSeries = regexp.MustCompile(`^o[0-9]`)

// endpointPreference lists the endpoints that suit a model, best first.
// Claude models want Anthropic; GPT, o-series and codex models want the
// OpenAI endpoints; any other model wants the client's own protocol, so
// nothing is translated.
func endpointPreference(from Protocol, model string) []Protocol {
	name := model
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "claude"):
		return []Protocol{Anthropic}
	case strings.Contains(name, "codex"):
		return openAIOrder(from, Responses, Chat)
	case strings.HasPrefix(name, "gpt"), openSeries.MatchString(name):
		return openAIOrder(from, Chat, Responses)
	}
	return []Protocol{from}
}

// openAIOrder puts the client's protocol first when it is one of the two.
func openAIOrder(from, first, second Protocol) []Protocol {
	if from == second {
		first, second = second, first
	}
	return []Protocol{first, second}
}

// pickEndpoint chooses the endpoint for a key: the first preferred one it
// has (rank 0), else the client's own or the first it has (rank 1).
func pickEndpoint(view Provider, from Protocol, pref []Protocol) (Protocol, int) {
	for _, pr := range pref {
		if view.Base(pr) != "" {
			return pr, 0
		}
	}
	if view.Base(from) != "" {
		return from, 1
	}
	return view.Speaks()[0], 1
}

// ---- managing keys --------------------------------------------------------------

// editKeys runs fn on the stored provider id and saves the file.
func editKeys(id string, fn func(p *Provider) error) error {
	if _, ok := find(Accounts(), id); ok {
		return fmt.Errorf("%s is a signed-in account; it has no keys to manage", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			if err := fn(&f.Providers[i]); err != nil {
				return err
			}
			return store(f)
		}
	}
	return fmt.Errorf("no provider %q", id)
}

// locate finds a key by fingerprint: -1 for the current key, else an index
// into Keys.
func (p *Provider) locate(ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if p.Key != "" && KeyRef(p.Key) == ref {
		return -1, nil
	}
	for i, k := range p.Keys {
		if KeyRef(k.Key) == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s has no key %q", p.ID, ref)
}

// firstOn is the index of the first enabled extra key, or -1.
func (p *Provider) firstOn() int {
	for i, k := range p.Keys {
		if !k.Off {
			return i
		}
	}
	return -1
}

// current is the current key as an extra-key entry.
func (p *Provider) current() ExtraKey {
	return ExtraKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol}
}

// makeCurrent sets k as the current key.
func (p *Provider) makeCurrent(k ExtraKey) {
	p.Key, p.KeyName, p.KeyProtocol = k.Key, k.Name, k.Protocol
}

// AddKey adds a key to a provider. The first key becomes the current one;
// later keys are enabled and go after the existing ones.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q; use chat, responses, anthropic or nothing", proto)
	}
	return editKeys(id, func(p *Provider) error {
		if p.Key == key {
			return errors.New("that key is already on " + p.ID)
		}
		for _, k := range p.Keys {
			if k.Key == key {
				return errors.New("that key is already on " + p.ID)
			}
		}
		k := ExtraKey{Name: strings.TrimSpace(name), Key: key, Protocol: proto}
		if p.Key == "" {
			p.makeCurrent(k)
			return nil
		}
		p.Keys = append(p.Keys, k)
		return nil
	})
}

// SetKeyProtocol limits a key to one protocol, or lifts the limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q; use chat, responses, anthropic or nothing", proto)
	}
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
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

// UseKey makes a key the current one. The key it replaces stays enabled,
// first among the extra keys; names and protocol limits go with their keys.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i < 0 {
			return nil
		}
		next := p.Keys[i]
		rest := append([]ExtraKey{p.current()}, p.Keys[:i]...)
		p.Keys = append(rest, p.Keys[i+1:]...)
		next.Off = false
		p.makeCurrent(next)
		return nil
	})
}

// SetKeyOn enables or disables a key. Disabling the current key hands the
// role to the next enabled key; the old one is kept, disabled.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
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
		j := p.firstOn()
		if j < 0 {
			return errors.New("the last enabled key cannot be turned off")
		}
		next := p.Keys[j]
		cur := p.current()
		cur.Off = true
		rest := append([]ExtraKey{cur}, p.Keys[:j]...)
		p.Keys = append(rest, p.Keys[j+1:]...)
		next.Off = false
		p.makeCurrent(next)
		return nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be removed; removing
// the current key hands the role to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
		if err != nil {
			return err
		}
		if i >= 0 {
			p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
			return nil
		}
		j := p.firstOn()
		if j < 0 {
			return errors.New("the last enabled key cannot be removed")
		}
		next := p.Keys[j]
		p.Keys = append(p.Keys[:j], p.Keys[j+1:]...)
		next.Off = false
		p.makeCurrent(next)
		return nil
	})
}

// RenameKey changes a key's name.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the key needs a name")
	}
	return editKeys(id, func(p *Provider) error {
		i, err := p.locate(keyRef)
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
