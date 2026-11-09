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
	Key    string `json:"key"`              // API key, as typed by the user

	// Base URLs, one per protocol the vendor serves natively. magpie appends
	// the usual paths: chat/responses bases end in /v1 (OpenAI style),
	// the Anthropic base is the root (what ANTHROPIC_BASE_URL takes).
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`

	// Models the user chose to expose. Empty means "the preset's picks, or
	// everything the vendor lists when that list is short".
	Models []string `json:"models,omitempty"`

	Catalog string `json:"catalog,omitempty"` // models.dev id, for names and reasoning levels
	Website string `json:"website,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`
}

type file struct {
	Providers []Provider  `json:"providers"`
	Excluded  []Exclusion `json:"excluded,omitempty"` // signed-in accounts the user removed
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
	f := load()
	stored := f.Providers
	picks := map[string][]string{}
	var out []Provider
	for _, p := range stored {
		p = normalize(p)
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			picks[p.ID] = p.Models
			continue
		}
		out = append(out, p)
	}
	for _, a := range Accounts() {
		if _, taken := find(out, a.ID); taken || hiddenIn(f, a.ID) {
			continue
		}
		a.Models = picks[a.ID]
		out = append(out, a)
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
	f := load()
	if a, ok := find(Accounts(), p.ID); ok {
		// an account keeps only the user's model picks; the rest is the
		// agent's own sign-in. Bringing back a removed account restores
		// the picks it had, unless new ones are given.
		picks := p.Models
		if hiddenIn(f, a.ID) && len(picks) == 0 {
			picks = picksIn(f, a.ID)
		}
		p = Provider{ID: a.ID, Models: picks}
		f.Excluded = withoutHidden(f.Excluded, a.ID)
	} else {
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return errors.New("a provider needs a base URL")
		}
		if p.Key == "" && !keyOptional(p) {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
	}
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = p
			return store(f)
		}
	}
	f.Providers = append(f.Providers, p)
	return store(f)
}

// Remove takes a provider out of magpie. A signed-in account is only hidden
// (see Hide): its model picks are kept and the agent stays signed in. A
// provider the user added is deleted.
func Remove(id string) error {
	if a, ok := find(Accounts(), id); ok {
		return Hide(a.ID)
	}
	return Delete(id)
}

// Hide stops offering a signed-in account from magpie. It leaves the agent's
// own credentials alone and keeps the model picks, so Save brings it back.
func Hide(id string) error {
	a, ok := find(Accounts(), id)
	if !ok || a.Account == nil {
		return fmt.Errorf("no account %q", id)
	}
	f := load()
	if hiddenIn(f, id) {
		return nil
	}
	f.Excluded = append(f.Excluded, Exclusion{Agent: a.Account.Agent, Provider: id, Why: "removed from magpie"})
	return store(f)
}

// hiddenIn reports whether the user removed the account with this id.
func hiddenIn(f file, id string) bool {
	for _, x := range f.Excluded {
		if x.Provider == id {
			return true
		}
	}
	return false
}

// withoutHidden drops the removal record for an account id.
func withoutHidden(xs []Exclusion, id string) []Exclusion {
	var out []Exclusion
	for _, x := range xs {
		if x.Provider != id {
			out = append(out, x)
		}
	}
	return out
}

// picksIn returns the model picks stored for an id that has no URL of its own.
func picksIn(f file, id string) []string {
	for _, p := range f.Providers {
		if p.ID == id && p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return p.Models
		}
	}
	return nil
}

// Delete removes a provider. For an account it forgets the model picks;
// signing out is the agent's job.
func Delete(id string) error {
	if _, ok := find(Accounts(), id); ok {
		f := load()
		keep := f.Providers[:0]
		for _, p := range f.Providers {
			if p.ID != id {
				keep = append(keep, p)
			}
		}
		f.Providers = keep
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

func cleanList(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
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

// IsOpenCode is true when a base URL lives on opencode.ai or one of its
// subdomains. Those gateways reject requests without a session header.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		h := HostOf(p.Base(pr))
		if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
			h = h[:i]
		}
		h = strings.TrimSuffix(h, ".")
		if h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai") {
			return true
		}
	}
	return false
}
