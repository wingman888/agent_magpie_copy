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

	// Gemini is the Gemini API. It is a client protocol only: dial serves it
	// to Gemini clients but never speaks it to a provider, so it is kept out
	// of Protocols.
	Gemini Protocol = "gemini"
)

// Protocols in the order dial prefers them when it has to translate. These
// are the upstream protocols; Gemini is client-only.
var Protocols = []Protocol{Chat, Responses, Anthropic}

// Provider is one configured vendor.
type Provider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Preset string `json:"preset,omitempty"` // preset this was created from, if any
	Key    string `json:"key"`              // API key, as typed by the user

	// Account is set for providers that are an agent's sign-in (codex,
	// copilot). It is derived from the agent's own files, never stored.
	Account *Account `json:"account,omitempty"`

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
}

type file struct {
	Providers []Provider `json:"providers"`
}

// onDisk is the form a provider takes in providers.json. An account keeps
// only its id and model picks: its name and sign-in belong to the agent.
func (p Provider) onDisk() any {
	if IsAccount(p.ID) {
		return struct {
			ID     string   `json:"id"`
			Models []string `json:"models,omitempty"`
		}{p.ID, p.Models}
	}
	return p
}

// diskFile is what store writes; load reads the same shape back as file.
type diskFile struct {
	Providers []any `json:"providers"`
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
	out := diskFile{Providers: []any{}}
	for _, pr := range f.Providers {
		out.Providers = append(out.Providers, pr.onDisk())
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

// All lists the configured providers in the order they were added, then the
// agent accounts that are signed in right now. Stored entries for claude
// are ignored, and stored picks for an account that is not signed in are
// left out with it.
func All() []Provider {
	var out []Provider
	saved := map[string]Provider{}
	for _, p := range load().Providers {
		p = normalize(p)
		switch {
		case p.ID == claudeID:
			continue
		case IsAccount(p.ID):
			saved[p.ID] = p
		default:
			out = append(out, p)
		}
	}
	return append(out, accounts(saved)...)
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

// Save adds or replaces a provider. For an agent account only the model
// picks are kept; the sign-in itself is never written by dial.
func Save(p Provider) error {
	p = normalize(p)
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == claudeID {
		return errors.New("claude is not a provider: " + claudeNote)
	}
	if IsAccount(p.ID) {
		return saveAccount(p)
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

// saveAccount stores the model picks of an agent account. A key or a base URL
// has no place on an account: the sign-in supplies both, so passing one is an
// error rather than something dial quietly throws away. The account's own
// base URL (the one the sign-in gives it) is allowed, so a provider read back
// with Find can be saved again with new picks.
func saveAccount(p Provider) error {
	if p.Key != "" {
		return fmt.Errorf("%s is signed in through its agent and takes no API key; sign in with the agent instead", p.ID)
	}
	own := Provider{}
	for _, a := range accounts(nil) {
		if a.ID == p.ID {
			own = a
		}
	}
	if (p.Chat != "" && p.Chat != own.Chat) || (p.Responses != "" && p.Responses != own.Responses) || (p.Anthropic != "" && p.Anthropic != own.Anthropic) {
		return fmt.Errorf("%s is signed in through its agent and takes no base URL; only its model picks can be saved", p.ID)
	}
	f := load()
	entry := Provider{ID: p.ID, Models: cleanList(p.Models)}
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = entry
			return store(f)
		}
	}
	f.Providers = append(f.Providers, entry)
	return store(f)
}

// Delete removes a provider. For an agent account this clears the model
// picks only: the account stays listed while the agent is still signed in,
// and dial never touches the agent's sign-in.
func Delete(id string) error {
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
		if IsAccount(id) {
			return nil
		}
		return fmt.Errorf("no provider %q", id)
	}
	f.Providers = keep
	return store(f)
}

// keyOptional is true for local servers, which usually have no key, and for
// agent accounts, whose sign-in stands in for one.
func keyOptional(p Provider) bool {
	if p.Account != nil {
		return true
	}
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
func (p Provider) Ready() bool { return p.Key != "" || keyOptional(p) }
