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

	// Account is set on providers that borrow another agent's sign-in (see
	// account.go). Such providers never store credentials; Key stays empty.
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

// pick is what dial keeps for an agent account: its id and the models chosen.
// Nothing else about the account is ever written.
type pick struct {
	ID     string   `json:"id"`
	Models []string `json:"models,omitempty"`
}

func store(f file) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	list := make([]any, 0, len(f.Providers))
	for _, q := range f.Providers {
		if q.Account != nil || isPick(q) {
			list = append(list, pick{ID: q.ID, Models: q.Models})
			continue
		}
		list = append(list, q)
	}
	b, err := json.MarshalIndent(struct {
		Providers []any `json:"providers"`
	}{list}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// All lists the configured providers in the order they were added, then the
// agent accounts dial offers. A stored model pick under an agent's id is not a
// provider on its own: it shows only while that agent is still signed in.
func All() []Provider {
	var out []Provider
	for _, p := range load().Providers {
		if isPick(p) {
			continue
		}
		out = append(out, normalize(p))
	}
	return append(out, Accounts()...)
}

// accountID reports whether an id is reserved for an agent account.
func accountID(id string) bool { return id == "codex" || id == "copilot" }

// signedIn reports whether the agent behind an account id is signed in.
func signedIn(id string) bool {
	for _, p := range Accounts() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// isPick is a stored record under an agent account's id that has no address of
// its own: only the models the user chose.
func isPick(p Provider) bool {
	return accountID(p.ID) && p.Chat == "" && p.Responses == "" && p.Anthropic == ""
}

// savePicks stores the model choice for an agent account. Its address, key and
// name are never kept; they come from the agent. A user's own provider under
// the same id keeps its address and only takes the new picks.
func savePicks(id string, models []string) error {
	if !accountID(id) {
		return fmt.Errorf("%q is not an agent account", id)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].Models = cleanList(models)
			return store(f)
		}
	}
	f.Providers = append(f.Providers, Provider{ID: id, Models: cleanList(models)})
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

// Save adds or replaces a provider.
func Save(p Provider) error {
	p = normalize(p)
	// An account provider (as Find returns it) saves its picks only: its address
	// and key are ignored. A bare record under an agent's id (no address) is picks
	// too. Any other save under an agent's id is picks only, unless the config already
	// holds the user's own addressed provider there (ownProvider): that one is an
	// ordinary edit. Whether the agent is signed in does not matter; no address or key
	// reaches the config for an account id (TestOwnProviderWinsOverAccount).
	if p.ID == "" && p.Account != nil {
		p.ID = p.Account.Agent
	}
	if p.Account != nil || isPick(p) || (accountID(p.ID) && !ownProvider(p.ID)) {
		return savePicks(p.ID, p.Models)
	}
	if p.ID == "" {
		p.ID = Slug(p.Name)
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

// Delete removes a provider. For an agent account it only clears the models
// chosen: the account stays offered while the agent is signed in.
func Delete(id string) error {
	f := load()
	if accountID(id) && !ownProvider(id) {
		return deletePicks(f, id)
	}
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

// deletePicks clears the model choice of an agent account. A record left
// behind by a sign-out is dropped entirely, so nothing half-formed remains.
func deletePicks(f file, id string) error {
	in := signedIn(id)
	at := -1
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			at = i
		}
	}
	switch {
	case in && at >= 0:
		f.Providers[at].Models = nil
		return store(f)
	case in:
		return nil
	case at >= 0:
		f.Providers = append(f.Providers[:at], f.Providers[at+1:]...)
		return store(f)
	}
	return fmt.Errorf("no provider %q", id)
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
func (p Provider) Ready() bool { return p.Account != nil || p.Key != "" || keyOptional(p) }
