// Package provider holds the model vendors magpie can reach: where each one
// lives, which protocols it speaks, the API key the user typed in, and which
// of its models should show up in the agents' pickers.
//
// Nothing here reads environment variables. A provider is exactly what the
// user entered, kept in ~/.config/magpie/providers.json (mode 0600).
package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
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

	// KeyName names the current key; KeyProtocol limits it to one protocol
	// ("" is any). Keys are the other keys, tried in order after it.
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

	// Headers are extra request headers for a custom provider, sent with
	// every request magpie makes to it (forwarding, the test, the model list).
	Headers map[string]string `json:"headers,omitempty"`

	// Fallback lists "provider/model" ids tried, in order, when this
	// provider fails before it has replied.
	Fallback []string `json:"fallback,omitempty"`

	// Catalog is one or more models.dev ids, comma-separated, searched in
	// order for names and reasoning levels.
	Catalog string `json:"catalog,omitempty"`
	Website string `json:"website,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`

	// Hidden is set on an account the user removed from magpie; the
	// agent stays signed in, magpie just leaves it alone.
	Hidden bool `json:"hidden,omitempty"`

	// Account is set when the provider is an agent the user signed in to
	// (see account.go); it is derived, never stored.
	Account *Account `json:"-"`
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
	if strings.HasPrefix(p.Icon, "file:") && IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
		return fmt.Errorf("icon %q is not an icon magpie stored", p.Icon)
	}
	if a, ok := find(Accounts(), p.ID); ok {
		// an account keeps only the user's model picks and fallback; the
		// rest is the agent's own sign-in. Saving it again brings a removed
		// one back.
		p = Provider{ID: a.ID, Models: p.Models, Fallback: p.Fallback}
	} else {
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return errors.New("a provider needs a base URL")
		}
		if p.Key == "" && !keyOptional(p) {
			return fmt.Errorf("%s needs an API key", p.Name)
		}
		if err := checkHeaders(p.Headers); err != nil {
			return err
		}
		if err := checkKeys(p); err != nil {
			return err
		}
	}
	f := load()
	replaced := false
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
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
	pruneIcons(f)
	return nil
}

// tokenRe is what an HTTP header name may contain (RFC 9110 "token").
var tokenRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// checkHeaders refuses header names HTTP cannot carry and values with line
// breaks, which would let one header smuggle another.
func checkHeaders(m map[string]string) error {
	for k, v := range m {
		if !tokenRe.MatchString(k) {
			return fmt.Errorf("header name %q is not a valid HTTP header name", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("header %s has a line break in its value", k)
		}
	}
	return nil
}

// cleanHeaders trims names and values and drops entries with no name. An
// empty map is nil, so providers.json does not carry it.
func cleanHeaders(m map[string]string) map[string]string {
	var out map[string]string
	for k, v := range m {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// ExtraHeaders are the headers the user set on a custom provider. Presets and
// signed-in accounts have none.
func (p Provider) ExtraHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// ApplyHeaders puts the provider's own headers on a request. Call it after
// the auth headers are set, so these can override the defaults.
func (p Provider) ApplyHeaders(req *http.Request) {
	catalog.ApplyHeaders(req, p.ExtraHeaders())
}

// Catalogs lists the models.dev ids the provider reads names and reasoning
// levels from, in the order they are searched.
func (p Provider) Catalogs() []string {
	var out []string
	for _, c := range strings.Split(p.Catalog, ",") {
		if c = strings.TrimSpace(c); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// MaxIcon is the largest picture magpie keeps as a provider icon.
const MaxIcon = 1 << 20

// IconDir is where stored icons live: icons/ beside providers.json.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// iconName is what a stored icon is called: 16 lowercase hex digits (the
// first 8 bytes of the content's SHA-256) and a known extension.
var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconFile turns a stored icon's file name into its path on disk. Anything
// that is not exactly a name StoreIcon could have written gives "".
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// StoreIcon keeps a picture as a provider icon and returns the value to put
// in Provider.Icon ("file:<name>"). The same picture is stored once; storing
// it again makes it fresh, so pruneIcons leaves it alone for an hour.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is larger than %d KB", MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can show: PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			return "", err
		}
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// sniffIcon names the format of a picture by its content, or "" when it is
// none of the formats magpie shows.
func sniffIcon(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif"
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return ".ico"
	case isSVG(b):
		return ".svg"
	}
	return ""
}

// isSVG is true when the document is an SVG picture: after any leading space
// and byte-order mark it opens with an XML declaration, a doctype, a comment or
// <svg>, and its first element is <svg>. Text that only mentions <svg> is not.
func isSVG(b []byte) bool {
	s := bytes.TrimLeft(b, " \t\r\n")
	s = bytes.TrimLeft(bytes.TrimPrefix(s, []byte("\xEF\xBB\xBF")), " \t\r\n")
	opens := false
	for _, p := range []string{"<?xml", "<!DOCTYPE", "<!--", "<svg"} {
		if bytes.HasPrefix(s, []byte(p)) {
			opens = true
			break
		}
	}
	if !opens {
		return false
	}
	d := xml.NewDecoder(bytes.NewReader(s))
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return strings.EqualFold(se.Name.Local, "svg")
		}
	}
}

// pruneIcons deletes stored icons that no provider refers to and that are
// over an hour old. The hour lets the user save a provider after choosing
// its picture.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if n, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[n] = true
		}
	}
	ents, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || used[name] || IconFile(name) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(IconDir(), name))
	}
}

// HeaderNames lists header names in a stable order, for display and
// for sending.
func HeaderNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
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
	p.KeyName = strings.TrimSpace(p.KeyName)
	p.Keys = cleanKeys(p.Keys)
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic, &p.Website, &p.KeysURL} {
		*u = strings.TrimRight(strings.TrimSpace(*u), "/")
		if *u != "" && !strings.Contains(*u, "://") {
			*u = "https://" + *u
		}
	}
	p.Models = cleanList(p.Models)
	p.Fallback = cleanList(p.Fallback)
	p.Catalog = strings.Join(p.Catalogs(), ",")
	p.Headers = cleanHeaders(p.Headers)
	if p.Preset != "" {
		p.Headers = nil // presets speak for themselves; extra headers are ignored
	}
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
