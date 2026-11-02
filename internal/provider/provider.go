// Package provider holds the model vendors dial can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Nothing here reads environment variables. A provider is exactly what the
// user entered, kept in ~/.config/dial/providers.json (mode 0600).
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

// Protocol is a wire API dial can speak to an upstream.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini; only served to clients, never spoken upstream
)

// Protocols in the order dial prefers them when it has to translate.
var Protocols = []Protocol{Chat, Responses, Anthropic}

// Provider is one configured vendor.
type Provider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Preset string `json:"preset,omitempty"` // preset this was created from, if any
	Key    string `json:"key"`              // API key, as typed by the user

	// Base URLs, one per protocol the vendor serves natively. dial appends
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

	// Account is set for a provider that is an agent's own sign-in (see
	// Accounts). It is never stored; the agent's files are the source.
	Account *Account `json:"account,omitempty"`
}

type file struct {
	Providers []Provider `json:"providers"`
}

// Path is the file the user's providers live in.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "dial", "providers.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dial", "providers.json")
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
	// an account keeps nothing but the user's model picks
	type pick struct {
		ID     string   `json:"id"`
		Models []string `json:"models,omitempty"`
	}
	var out struct {
		Providers []any `json:"providers"`
	}
	out.Providers = []any{}
	for _, p := range f.Providers {
		if accountID(p.ID) && !hasBase(p) {
			out.Providers = append(out.Providers, pick{ID: p.ID, Models: p.Models})
			continue
		}
		p.Account = nil
		out.Providers = append(out.Providers, p)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// All lists the configured providers in the order they were added, then
// the signed-in agent accounts (see Accounts). A provider the user built
// with an account's id wins over the account.
func All() []Provider {
	var out []Provider
	picks := map[string][]string{}
	custom := map[string]bool{}
	for _, p := range load().Providers {
		p.Account = nil
		p = normalize(p)
		if accountID(p.ID) && !hasBase(p) {
			// the model picks of an account; nothing without the sign-in
			picks[p.ID] = p.Models
			continue
		}
		custom[p.ID] = true
		out = append(out, p)
	}
	for _, a := range signedIn() {
		if custom[a.ID] {
			continue
		}
		a = normalize(a)
		a.Models = cleanList(picks[a.ID])
		out = append(out, a)
	}
	return out
}

// storedCustom reports whether the user built a provider with this id.
func storedCustom(id string) bool {
	for _, p := range load().Providers {
		if strings.ToLower(strings.TrimSpace(p.ID)) == id && hasBase(normalize(p)) {
			return true
		}
	}
	return false
}

// signedInAccount is the account provider with this id, when the agent is
// signed in and no provider the user built shadows it.
func signedInAccount(id string) (Provider, bool) {
	if !accountID(id) || storedCustom(id) {
		return Provider{}, false
	}
	for _, a := range signedIn() {
		if a.ID == id {
			return a, true
		}
	}
	return Provider{}, false
}

// saveAccount keeps only the model picks of an account provider.
func saveAccount(id string, models []string) error {
	rec := Provider{ID: id, Models: cleanList(models)}
	f := load()
	for i := range f.Providers {
		if strings.ToLower(strings.TrimSpace(f.Providers[i].ID)) == id {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// Find looks a provider up by id (or name, case-insensitively).
func Find(id string) (*Provider, error) {
	q := strings.ToLower(strings.TrimSpace(id))
	for _, p := range All() {
		if p.ID == q || strings.ToLower(p.Name) == q {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no provider %q — dial providers lists them", id)
}

var idRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug derives an id from a name: "My Relay" → "my-relay".
func Slug(name string) string {
	return strings.Trim(idRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Save adds or replaces a provider. For a signed-in account only the model
// picks are kept; a URL, key or anything else passed along is ignored.
func Save(p Provider) error {
	p = normalize(p)
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if _, ok := signedInAccount(p.ID); ok {
		return saveAccount(p.ID, p.Models)
	}
	if p.ID == "" || p.ID != Slug(p.ID) {
		return fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", p.ID)
	}
	if p.ID == "dial" {
		return errors.New(`"dial" is what agents call the gateway itself; pick another id`)
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return errors.New("a provider needs a base URL")
	}
	if p.Key == "" && !keyOptional(p) {
		return fmt.Errorf("%s needs an API key", p.Name)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = p
			return store(f)
		}
	}
	f.Providers = append(f.Providers, p)
	return store(f)
}

// Delete removes a provider. For a signed-in account it only forgets the
// model picks: signing out is the agent's business.
func Delete(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := signedInAccount(id); ok {
		f := load()
		keep := f.Providers[:0]
		for _, p := range f.Providers {
			if strings.ToLower(strings.TrimSpace(p.ID)) != id {
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

// Ready reports whether the provider can be used: it has a key, or needs none.
// An account is always ready: the agent's sign-in stands in for a key.
func (p Provider) Ready() bool { return p.Account != nil || p.Key != "" || keyOptional(p) }
