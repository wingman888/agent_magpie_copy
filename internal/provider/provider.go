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
	Name   string `json:"name,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Preset string `json:"preset,omitempty"` // preset this was created from, if any
	Key    string `json:"key,omitempty"`    // API key, as typed by the user

	// Account is set on a provider that is a sign-in the user already has in
	// an agent (codex, copilot). Such a provider keeps no address or key; its
	// Account is read from the agent's files and never stored.
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
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// hasAddress is true for a provider the user built with base URLs of its own.
func (p Provider) hasAddress() bool {
	return p.Chat != "" || p.Responses != "" || p.Anthropic != ""
}

// find returns the index of a stored record by id, or -1.
func find(f file, id string) int {
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			return i
		}
	}
	return -1
}

// All lists the configured providers in the order they were added, then the
// signed-in account providers that have no record yet. A stored account
// record is only a model choice; it is listed while its sign-in lasts.
func All() []Provider {
	recs := load().Providers
	var out []Provider
	seenAccount := map[string]bool{}
	for _, r := range recs {
		p := normalize(r)
		if !isAccountID(p.ID) || p.hasAddress() {
			out = append(out, p)
			continue
		}
		if seenAccount[p.ID] {
			continue
		}
		seenAccount[p.ID] = true
		if acc, ok := signedIn(p.ID); ok {
			acc.Models = p.Models
			out = append(out, acc)
		}
	}
	for _, id := range accountIDs {
		if seenAccount[id] || hasSelfBuilt(recs, id) {
			continue
		}
		if acc, ok := signedIn(id); ok {
			out = append(out, acc)
		}
	}
	return out
}

// hasSelfBuilt reports whether the user built a provider with this id that
// has addresses of its own; that one wins over the account.
func hasSelfBuilt(recs []Provider, id string) bool {
	for _, r := range recs {
		if r.ID == id && normalize(r).hasAddress() {
			return true
		}
	}
	return false
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

// Save adds or replaces a provider. For an account provider (codex, copilot)
// only the model choice is kept; the address, key and other fields passed in
// are ignored, unless the user already has their own provider with that id.
func Save(p Provider) error {
	p = normalize(p)
	if p.ID == "" {
		p.ID = Slug(p.Name)
	}
	// A user's own provider with this id is kept as it was saved; the
	// account path only ever stores the model choice.
	if isAccountID(p.ID) && !(p.hasAddress() && hasSelfBuilt(load().Providers, p.ID)) {
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

// saveAccount stores the model choice for an account provider. Only the
// choice is written, never the address or key.
func saveAccount(p Provider) error {
	if _, ok := signedIn(p.ID); !ok {
		return fmt.Errorf("%s is not signed in; sign in with its agent first", p.ID)
	}
	f := load()
	if i := find(f, p.ID); i >= 0 && f.Providers[i].hasAddress() {
		// the user's own provider with this id wins; only its models change
		f.Providers[i].Models = p.Models
		return store(f)
	}
	rec := Provider{ID: p.ID, Models: p.Models}
	if i := find(f, p.ID); i >= 0 {
		f.Providers[i] = rec
	} else {
		f.Providers = append(f.Providers, rec)
	}
	return store(f)
}

// deleteAccount clears an account provider's model choice; the provider stays
// while its sign-in does. Once signed out, its record is dropped.
func deleteAccount(f file, id string) error {
	_, signed := signedIn(id)
	i := find(f, id)
	if !signed {
		if i < 0 {
			return fmt.Errorf("no provider %q", id)
		}
		f.Providers = append(f.Providers[:i], f.Providers[i+1:]...)
		return store(f)
	}
	if i >= 0 {
		f.Providers[i].Models = nil
		return store(f)
	}
	return nil
}

// Delete removes a provider. For an account provider it only clears the
// model choice.
func Delete(id string) error {
	f := load()
	if i := find(f, id); isAccountID(id) && (i < 0 || !f.Providers[i].hasAddress()) {
		return deleteAccount(f, id)
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

// keyOptional is true for local servers, which usually have no key.
func keyOptional(p Provider) bool {
	if pr := Preset(p.Preset); pr != nil && pr.NoKey {
		return true
	}
	h := p.Host()
	return strings.HasPrefix(h, "localhost") || strings.HasPrefix(h, "127.0.0.1") || strings.HasPrefix(h, "0.0.0.0")
}

func normalize(p Provider) Provider {
	// An Account is never read from or written to providers.json. It is
	// always taken fresh from the agent's own sign-in (see signedIn).
	p.Account = nil
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
// An account provider is ready while its sign-in exists, which All checks.
func (p Provider) Ready() bool { return p.Account != nil || p.Key != "" || keyOptional(p) }
