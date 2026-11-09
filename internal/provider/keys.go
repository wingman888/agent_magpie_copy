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

// SubKey is an API key kept beside the provider's current one. The current
// key lives in Provider.Key (named by KeyName, limited by KeyProtocol); the
// others sit in Provider.Keys in the order the user gave them.
type SubKey struct {
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key"`
	Protocol Protocol `json:"protocol,omitempty"` // "" = usable on any endpoint
	Off      bool     `json:"off,omitempty"`      // disabled: never tried
}

// KeyRef is a key's fingerprint: the first five bytes of its SHA-256, in
// lower-case hex. It is how the CLI and the call log name a key without
// showing it.
func KeyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// validKeyProtocol accepts "" (any endpoint) or a protocol magpie speaks upstream.
func validKeyProtocol(proto Protocol) bool {
	switch proto {
	case "", Chat, Responses, Anthropic:
		return true
	}
	return false
}

// Credential is one enabled key of a provider, with the endpoint it is
// limited to ("" = any).
type Credential struct {
	Ref      string
	Name     string
	Key      string
	Protocol Protocol
}

// KeyStatus describes one stored key for display. The key itself is masked.
type KeyStatus struct {
	Ref      string
	Name     string
	Masked   string
	Protocol Protocol
	Off      bool
	Current  bool
}

// keyList is every stored key, the current one first, disabled ones included.
func (p Provider) keyList() []SubKey {
	var out []SubKey
	if p.Key != "" {
		out = append(out, SubKey{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol})
	}
	return append(out, p.Keys...)
}

// setKeyList stores a key list back. The first enabled key becomes the
// current one; a disabled one ahead of it keeps its place in Keys.
func (p *Provider) setKeyList(list []SubKey) {
	promote(list)
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = "", "", "", nil
	if len(list) == 0 {
		return
	}
	if list[0].Off {
		// nothing is enabled; keep the keys but leave no current one
		p.Keys = list
		return
	}
	p.Key, p.KeyName, p.KeyProtocol = list[0].Key, list[0].Name, list[0].Protocol
	if len(list) > 1 {
		p.Keys = list[1:]
	}
}

// promote moves the first enabled key to the front, when the front one is
// disabled. The disabled key moves right behind it.
func promote(list []SubKey) {
	if len(list) == 0 || !list[0].Off {
		return
	}
	for j := 1; j < len(list); j++ {
		if !list[j].Off {
			chosen := list[j]
			copy(list[1:j+1], list[0:j])
			list[0] = chosen
			return
		}
	}
}

func enabledCount(list []SubKey, skip int) int {
	n := 0
	for i, k := range list {
		if i != skip && !k.Off {
			n++
		}
	}
	return n
}

func findKey(list []SubKey, ref string) int {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for i, k := range list {
		if KeyRef(k.Key) == ref {
			return i
		}
	}
	return -1
}

// Credentials lists the enabled keys, the current one first. A key whose
// protocol is not one the provider speaks is left out only by Candidates.
func (p Provider) Credentials() []Credential {
	var out []Credential
	for _, k := range p.keyList() {
		if k.Off || k.Key == "" || !validKeyProtocol(k.Protocol) {
			continue
		}
		out = append(out, Credential{Ref: KeyRef(k.Key), Name: k.Name, Key: k.Key, Protocol: k.Protocol})
	}
	return out
}

// AllKeys lists every stored key for display, current first.
func (p Provider) AllKeys() []KeyStatus {
	var out []KeyStatus
	for i, k := range p.keyList() {
		out = append(out, KeyStatus{Ref: KeyRef(k.Key), Name: k.Name, Masked: Mask(k.Key),
			Protocol: k.Protocol, Off: k.Off, Current: i == 0 && p.Key != ""})
	}
	return out
}

// editKeys applies change to a stored provider's keys and writes the file.
func editKeys(id string, change func(list []SubKey) ([]SubKey, error)) error {
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		p := &f.Providers[i]
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return fmt.Errorf("%s is a signed-in account; it has no API keys to manage", id)
		}
		list, err := change(p.keyList())
		if err != nil {
			return err
		}
		p.setKeyList(list)
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// AddKey adds a key to a provider. It is enabled and goes after the keys
// already there, unless the provider had none, in which case it is current.
func AddKey(id, name, key string, proto Protocol) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic, or leave it empty", proto)
	}
	name = strings.TrimSpace(name)
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		for _, k := range list {
			if k.Key == key {
				return nil, fmt.Errorf("that key is already on %s (%s)", id, KeyRef(key))
			}
		}
		return append(list, SubKey{Name: name, Key: key, Protocol: proto}), nil
	})
}

// SetKeyProtocol limits a key to one protocol's endpoint, or clears the
// limit with "".
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if !validKeyProtocol(proto) {
		return fmt.Errorf("unknown key protocol %q: use chat, responses, anthropic, or leave it empty", proto)
	}
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		i := findKey(list, keyRef)
		if i < 0 {
			return nil, fmt.Errorf("no key %q on %s", keyRef, id)
		}
		list[i].Protocol = proto
		return list, nil
	})
}

// UseKey makes a key the current one. The key it replaces stays enabled,
// right after it; every key keeps its name and protocol limit.
func UseKey(id, keyRef string) error {
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		i := findKey(list, keyRef)
		if i < 0 {
			return nil, fmt.Errorf("no key %q on %s", keyRef, id)
		}
		if i == 0 {
			return list, nil
		}
		chosen := list[i]
		chosen.Off = false
		out := []SubKey{chosen, list[0]}
		out[1].Off = false
		for j, k := range list {
			if j != 0 && j != i {
				out = append(out, k)
			}
		}
		return out, nil
	})
}

// SetKeyOn enables or disables a key. The last enabled key cannot be
// disabled. Disabling the current key hands the role to the next enabled one.
func SetKeyOn(id, keyRef string, on bool) error {
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		i := findKey(list, keyRef)
		if i < 0 {
			return nil, fmt.Errorf("no key %q on %s", keyRef, id)
		}
		if on {
			list[i].Off = false
			return list, nil
		}
		if list[i].Off {
			return list, nil
		}
		if enabledCount(list, i) == 0 {
			return nil, fmt.Errorf("%s needs at least one enabled key; add or enable another before turning this one off", id)
		}
		list[i].Off = true
		promote(list)
		return list, nil
	})
}

// RemoveKey deletes a key. The last enabled key cannot be deleted. Deleting
// the current key hands the role to the next enabled one.
func RemoveKey(id, keyRef string) error {
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		i := findKey(list, keyRef)
		if i < 0 {
			return nil, fmt.Errorf("no key %q on %s", keyRef, id)
		}
		if !list[i].Off && enabledCount(list, i) == 0 {
			return nil, fmt.Errorf("%s needs at least one enabled key; this is its last one", id)
		}
		out := append(append([]SubKey{}, list[:i]...), list[i+1:]...)
		promote(out)
		return out, nil
	})
}

// RenameKey changes the name a key is shown under.
func RenameKey(id, keyRef, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the name is empty")
	}
	return editKeys(id, func(list []SubKey) ([]SubKey, error) {
		i := findKey(list, keyRef)
		if i < 0 {
			return nil, fmt.Errorf("no key %q on %s", keyRef, id)
		}
		list[i].Name = name
		return list, nil
	})
}

// openAIModel matches o-series model ids such as o3 or o4-mini.
var openAIModel = regexp.MustCompile(`^o\d`)

// family names the kind of model, from its id without any vendor prefix.
func family(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"), openAIModel.MatchString(m):
		return "openai"
	}
	return "other"
}

// preferred lists the endpoints a request for this family would rather use.
func preferred(fam string, from Protocol) []Protocol {
	switch fam {
	case "claude":
		return []Protocol{Anthropic}
	case "openai":
		return []Protocol{Chat, Responses}
	}
	return []Protocol{from}
}

// route is the endpoint a request goes to when the key has no limit: the
// client's own protocol when the vendor speaks it, else the first it speaks.
func (p Provider) route(from Protocol) Protocol {
	if p.Base(from) != "" {
		return from
	}
	if s := p.Speaks(); len(s) > 0 {
		return s[0]
	}
	return ""
}

// Candidate is one way to send a request: the provider carrying a single
// key (empty when the vendor needs none), and the endpoint to send to.
type Candidate struct {
	Provider
	Ref   string   // the key's fingerprint, "" for the no-key route
	To    Protocol // the endpoint the request is sent to
	Relay bool     // the request goes to the client's own protocol as-is, not translated
}

// Candidates orders the ways to serve a request from this provider: its
// enabled keys, each on the endpoint its limit allows, most suitable first.
// A key restricted to a protocol the vendor lacks is left out. A key whose
// endpoint suits the model family (or the client's own protocol) comes first;
// other keys follow as a last resort. Keys keep their stored order within
// a rank. A provider with no keys gets one candidate with no key.
func (p Provider) Candidates(from Protocol, model string) []Candidate {
	creds := p.Credentials()
	if len(creds) == 0 {
		creds = []Credential{{}}
	}
	want := preferred(family(model), from)
	var out []Candidate
	for _, c := range creds {
		cand := Candidate{Provider: p, Ref: c.Ref}
		cand.Key, cand.KeyName, cand.KeyProtocol = c.Key, c.Name, c.Protocol
		if c.Protocol != "" {
			if p.Base(c.Protocol) == "" {
				continue
			}
			cand.To = c.Protocol
		} else {
			cand.To = p.route(from)
			if cand.To == "" {
				continue
			}
		}
		cand.Relay = cand.To == from
		out = append(out, cand)
	}
	rank := func(c Candidate) int {
		for _, w := range want {
			if c.To == w {
				return 0
			}
		}
		if c.KeyProtocol != "" {
			return 2
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// cleanKeys trims key names and drops empty keys. Used on load and save.
func cleanKeys(p Provider) Provider {
	p.KeyName = strings.TrimSpace(p.KeyName)
	p.Key = strings.TrimSpace(p.Key)
	var keys []SubKey
	for _, k := range p.Keys {
		k.Name = strings.TrimSpace(k.Name)
		k.Key = strings.TrimSpace(k.Key)
		if k.Key != "" {
			keys = append(keys, k)
		}
	}
	p.Keys = keys
	return p
}

// checkKeys rejects protocol values other than the four allowed, and
// duplicate keys.
func checkKeys(p Provider) error {
	if !validKeyProtocol(p.KeyProtocol) {
		return fmt.Errorf("key protocol %q is not chat, responses, anthropic, or empty", p.KeyProtocol)
	}
	seen := map[string]bool{}
	for _, k := range p.keyList() {
		if !validKeyProtocol(k.Protocol) {
			return fmt.Errorf("key protocol %q is not chat, responses, anthropic, or empty", k.Protocol)
		}
		if seen[k.Key] {
			return fmt.Errorf("%s lists the same key twice", p.Name)
		}
		seen[k.Key] = true
	}
	return nil
}

// carryKeys keeps a provider's extra keys when it is saved again whole, as
// the GUI and "provider add" do. A changed current key replaces the old one.
func carryKeys(p *Provider, old Provider) {
	if p.Key == old.Key {
		if p.KeyName == "" {
			p.KeyName = old.KeyName
		}
		if p.KeyProtocol == "" {
			p.KeyProtocol = old.KeyProtocol
		}
		if p.Keys == nil {
			p.Keys = old.Keys
		}
		return
	}
	src := p.Keys
	if src == nil {
		src = old.Keys
	}
	var keep []SubKey
	for _, k := range src {
		if k.Key != p.Key {
			keep = append(keep, k)
		}
	}
	p.Keys = keep
}
