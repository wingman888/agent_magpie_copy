// Package provider holds the model vendors magpie can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Nothing here reads environment variables. A provider is exactly what the
// user entered, kept in ~/.config/magpie/providers.json (mode 0600).
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Protocol is a wire API magpie can speak to an upstream.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini; only served to clients, never spoken upstream
)

// Protocols in the order magpie prefers them when it has to translate.
var Protocols = []Protocol{Chat, Responses, Anthropic}

// Provider is one configured vendor.
type Provider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Preset string `json:"preset,omitempty"` // preset this was created from, if any
	Key    string `json:"key"`              // current API key, as typed by the user

	// KeyName names the current key, KeyProtocol limits it to one protocol
	// (empty: any). Keys holds the other keys, in order; see keys.go.
	KeyName     string     `json:"keyName,omitempty"`
	KeyProtocol Protocol   `json:"keyProtocol,omitempty"`
	Keys        []KeyEntry `json:"keys,omitempty"`

	// Base URLs, one per protocol the vendor serves natively. magpie appends
	// the usual paths: chat/responses bases end in /v1 (OpenAI style),
	// the Anthropic base is the root (what ANTHROPIC_BASE_URL takes).
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`

	// Models the user chose to expose. Empty means "the preset's picks, or
	// everything the vendor lists when that list is short".
	Models []string `json:"models,omitempty"`

	// Fallback lists "provider/model" entries tried, in order, when this
	// provider cannot serve a request (quota, rate limit, outage). See
	// SetFallback and the gateway's candidate chain.
	Fallback []string `json:"fallback,omitempty"`

	// Headers are extra request headers sent with every request to a custom
	// provider (forwarding, tests, model lists). Names keep the case the user
	// typed. Presets and signed-in accounts ignore them; see ExtraHeaders.
	Headers map[string]string `json:"headers,omitempty"`

	// Catalog is one or more models.dev ids, comma separated, searched in
	// order for names and reasoning levels. See Catalogs.
	Catalog string `json:"catalog,omitempty"`
	Website string `json:"website,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`

	// Hidden is set on an account the user removed from magpie; the
	// agent stays signed in, magpie just leaves it alone.
	Hidden bool `json:"hidden,omitempty"`

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`

	// FallbackModel is the model name a fallback candidate sends upstream in
	// place of the request's. It is set on candidates only, never stored.
	FallbackModel string `json:"-"`
}

type file struct {
	Providers []Provider `json:"providers"`
}

// Path is the file the user's providers live in.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "providers.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "providers.json")
}

func load() file {
	var f file
	if b, err := os.ReadFile(Path()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func store(f file) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// All lists the configured providers in the order they were added, then
// the signed-in agents. An entry in the file with no URL is only the
// model picks for one of those accounts.
func All() []Provider {
	stored := load().Providers
	picks := map[string]Provider{}
	var out []Provider
	for _, p := range stored {
		p = normalize(p)
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			picks[p.ID] = p
			continue
		}
		out = append(out, p)
	}
	for _, a := range Accounts() {
		if _, taken := find(out, a.ID); taken || picks[a.ID].Hidden {
			continue
		}
		a.Models = picks[a.ID].Models
		a.Fallback = picks[a.ID].Fallback
		out = append(out, a)
	}
	return out
}

// Hidden lists the signed-in accounts the user removed from magpie.
func Hidden() []Provider {
	var out []Provider
	for _, a := range Accounts() {
		for _, p := range load().Providers {
			if p.ID == a.ID && p.Hidden {
				out = append(out, a)
			}
		}
	}
	return out
}

func find(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Find looks a provider up by id (or name, case-insensitively).
func Find(id string) (*Provider, error) {
	q := strings.ToLower(strings.TrimSpace(id))
	for _, p := range All() {
		if p.ID == q || strings.ToLower(p.Name) == q {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no provider %q — magpie providers lists them", id)
}

var idRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug derives an id from a name: "My Relay" → "my-relay".
func Slug(name string) string {
	return strings.Trim(idRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Save adds or replaces a provider.
func Save(p Provider) error {
	p = normalize(p)
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" || p.ID != Slug(p.ID) {
		return fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", p.ID)
	}
	if p.ID == "magpie" {
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if err := checkIcon(p.Icon); err != nil {
		return err
	}
	if a, ok := find(Accounts(), p.ID); ok {
		// an account keeps only the user's model picks; the rest is the
		// agent's own sign-in. Saving it again brings a removed one back.
		p = Provider{ID: a.ID, Models: p.Models, Fallback: p.Fallback}
	} else {
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return errors.New("a provider needs a base URL")
		}
		if p.Key == "" && !keyOptional(p) {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
		if !validKeyProtocol(p.KeyProtocol) {
			return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", p.KeyProtocol)
		}
		for _, k := range p.Keys {
			if !validKeyProtocol(k.Protocol) {
				return fmt.Errorf("key protocol must be chat, responses, anthropic or empty, not %q", k.Protocol)
			}
		}
	}
	f := load()
	replaced := false
	for i := range f.Providers {
		if old := f.Providers[i]; old.ID == p.ID {
			// Keys change through the key functions, so a save that does not
			// name them keeps what is stored. The fallback list works the same
			// way; SetFallback changes it.
			if p.Keys == nil {
				p.Keys = old.Keys
			}
			if p.Fallback == nil {
				p.Fallback = old.Fallback
			}
			if p.Key == old.Key {
				if p.KeyName == "" {
					p.KeyName = old.KeyName
				}
				if p.KeyProtocol == "" {
					p.KeyProtocol = old.KeyProtocol
				}
			}
			normalizeKeys(&p)
			f.Providers[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		f.Providers = append(f.Providers, p)
	}
	if err := store(f); err != nil {
		return err
	}
	pruneIcons()
	return nil
}

// Delete removes a provider. An account is only hidden from magpie (its
// model picks kept); signing out is the agent's job.
func Delete(id string) error {
	if _, ok := find(Accounts(), id); ok {
		f := load()
		for i := range f.Providers {
			if f.Providers[i].ID == id {
				f.Providers[i].Hidden = true
				return store(f)
			}
		}
		f.Providers = append(f.Providers, Provider{ID: id, Hidden: true})
		return store(f)
	}
	f := load()
	keep := f.Providers[:0]
	found := false
	for _, p := range f.Providers {
		if p.ID == id {
			found = true
			continue
		}
		keep = append(keep, p)
	}
	if !found {
		return fmt.Errorf("no provider %q", id)
	}
	f.Providers = keep
	return store(f)
}

// keyOptional is true for local servers, which usually have no key.
func keyOptional(p Provider) bool {
	if pr := Preset(p.Preset); pr != nil && pr.NoKey {
		return true
	}
	h := p.Host()
	return strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.0.0.1") || strings.HasPrefix(h, "0.0.0.0")
}

func normalize(p Provider) Provider {
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	p.Key = strings.TrimSpace(p.Key)
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic, &p.Website, &p.KeysURL} {
		*u = strings.TrimRight(strings.TrimSpace(*u), "/")
		if *u != "" && !strings.Contains(*u, "://") {
			*u = "https://" + *u
		}
	}
	p.Models = cleanList(p.Models)
	p.Fallback = cleanList(p.Fallback)
	p.Catalog = strings.Join(SplitCatalogs(p.Catalog), ",")
	p.Headers = cleanHeaders(p.Headers)
	normalizeKeys(&p)
	if pr := Preset(p.Preset); pr != nil {
		if p.Icon == "" {
			p.Icon = pr.Icon
		}
		if p.Catalog == "" {
			p.Catalog = pr.Catalog
		}
		if p.Website == "" {
			p.Website = pr.Website
		}
		if p.KeysURL == "" {
			p.KeysURL = pr.KeysURL
		}
	}
	return p
}

// SplitCatalogs parses a `catalog=` value: comma separated models.dev ids.
// Blanks are dropped, ids are lower-cased and duplicates removed, order kept.
func SplitCatalogs(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// Catalogs lists the models.dev catalogs the provider reads, in search order.
func (p Provider) Catalogs() []string { return SplitCatalogs(p.Catalog) }

// PrimaryCatalog is the first catalog, the one that names the vendor's icon
// and price; "" when the provider has none.
func (p Provider) PrimaryCatalog() string {
	if c := p.Catalogs(); len(c) > 0 {
		return c[0]
	}
	return ""
}

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// cleanHeaders trims names and values, drops entries with no name, and
// returns nil for an empty map so nothing is written for it.
func cleanHeaders(hs map[string]string) map[string]string {
	var out map[string]string
	for k, v := range hs {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(hs))
		}
		out[k] = v
	}
	return out
}

// ExtraHeaders are the request headers the user set on a custom provider.
// Presets and signed-in accounts send none.
func (p Provider) ExtraHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// ApplyHeaders sets the provider's extra headers on req. Call it after
// Sign, so a header the user sets can override a default. Names are written
// exactly as given (no canonical case), replacing any default of the same
// name.
func (p Provider) ApplyHeaders(req *http.Request) {
	for k, v := range p.ExtraHeaders() {
		req.Header.Del(k)
		req.Header[k] = []string{v}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Base returns the base URL for a protocol, or "" when the vendor lacks it.
func (p Provider) Base(proto Protocol) string {
	switch proto {
	case Chat:
		return p.Chat
	case Responses:
		return p.Responses
	case Anthropic:
		return p.Anthropic
	}
	return ""
}

// Speaks lists the protocols the vendor serves natively, preferred first.
func (p Provider) Speaks() []Protocol {
	var out []Protocol
	for _, pr := range Protocols {
		if p.Base(pr) != "" {
			out = append(out, pr)
		}
	}
	return out
}

// Host is the vendor's API host, for display.
func (p Provider) Host() string {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" {
			return HostOf(u)
		}
	}
	return ""
}

// IsOpenCode reports whether the provider is OpenCode's gateway (Zen or Go),
// which routes and caches by conversation and turns away requests that do
// not name one in x-opencode-session.
func (p Provider) IsOpenCode() bool {
	h := p.Host()
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// HostOf pulls the host out of a URL, for display.
func HostOf(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

// Mask hides all but the ends of a secret.
func Mask(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// Ready reports whether the provider can be used: it has a key, needs
// none, or is a signed-in agent.
func (p Provider) Ready() bool { return p.Account != nil || p.Key != "" || keyOptional(p) }

// SetFallback replaces a provider's fallback list: the "provider/model"
// entries tried, in order, when it cannot serve a request. Entries are
// trimmed, and empty or repeated ones dropped; an empty list clears it.
// Signed-in accounts keep a list too.
func SetFallback(id string, refs []string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	refs = cleanList(refs)
	f := load()
	if _, ok := find(Accounts(), id); ok {
		for i := range f.Providers {
			if f.Providers[i].ID == id {
				f.Providers[i].Fallback = refs
				return store(f)
			}
		}
		f.Providers = append(f.Providers, Provider{ID: id, Fallback: refs})
		return store(f)
	}
	for i := range f.Providers {
		sp := &f.Providers[i]
		if sp.ID == id && (sp.Chat != "" || sp.Responses != "" || sp.Anthropic != "") {
			sp.Fallback = refs
			return store(f)
		}
	}
	return fmt.Errorf("no provider %q", id)
}

// SplitFallback reads a fallback entry, "provider/model". Provider ids have
// no slash, so the first one ends the provider part and the model may hold
// more. ok is false when either part is missing.
func SplitFallback(ref string) (id, model string, ok bool) {
	id, model, ok = strings.Cut(strings.TrimSpace(ref), "/")
	id, model = strings.TrimSpace(id), strings.TrimSpace(model)
	if !ok || id == "" || model == "" {
		return "", "", false
	}
	return id, model, true
}
