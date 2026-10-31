package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// Import links: magpie://import?preset=deepseek&key=… adds a provider.
// ParseImport only reads the link; saving is up to the caller, after the
// user has seen what is about to be added.

const (
	maxImportName   = 80
	maxImportKey    = 4096
	maxImportModels = 200
)

// importAction strips the scheme and action from a link and returns its query.
func importQuery(link string) (string, error) {
	s := strings.TrimSpace(link)
	if len(s) < 7 || !strings.EqualFold(s[:7], "magpie:") {
		return "", errors.New("not a magpie link")
	}
	s = strings.TrimLeft(s[7:], "/")
	if len(s) < 6 || !strings.EqualFold(s[:6], "import") {
		return "", errors.New("unsupported magpie link: only magpie://import is understood")
	}
	s = s[6:]
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return "", errors.New("the import link has no parameters")
	}
	if s[0] != '?' {
		return "", errors.New("unsupported magpie link: only magpie://import is understood")
	}
	return s[1:], nil
}

// ParseImport turns an import link into the provider it describes. It
// saves nothing; any invalid input rejects the whole link.
func ParseImport(link string) (Provider, error) {
	raw, err := importQuery(link)
	if err != nil {
		return Provider{}, err
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %w", err)
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	presetID := strings.ToLower(get("preset"))
	if presetID != "" {
		if p, err = FromPreset(presetID); err != nil {
			return Provider{}, err
		}
	}
	if r := get("region"); r != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("region %q needs a preset", r)
		}
		found := false
		for _, rg := range pr.Regions {
			if strings.EqualFold(rg.ID, r) {
				p.Chat, p.Responses, p.Anthropic = rg.Chat, rg.Responses, rg.Anthropic
				found = true
				break
			}
		}
		if !found {
			return Provider{}, fmt.Errorf("%s has no region %q", pr.Name, r)
		}
	}

	if n := cleanName(q.Get("name")); n != "" {
		p.Name = n
	} else if presetID == "" {
		return Provider{}, errors.New("the link needs a name")
	}

	for _, b := range []struct {
		key string
		dst *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		v := get(b.key)
		if v == "" {
			continue
		}
		u, err := importBase(v)
		if err != nil {
			return Provider{}, fmt.Errorf("%s: %w", b.key, err)
		}
		*b.dst = u
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("the link needs at least one of chat, responses or anthropic")
	}

	id := q.Get("id")
	switch {
	case strings.TrimSpace(id) != "":
		id = strings.TrimSpace(id)
		if id != Slug(id) {
			return Provider{}, fmt.Errorf("id must be lowercase letters, digits and dashes, not %q", id)
		}
	case presetID != "":
		id = presetID
	default:
		id = Slug(p.Name)
	}
	if id == "" {
		return Provider{}, errors.New("cannot derive an id from that name; pass id=")
	}
	if id == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if presetID == "" && (id == "claude" || id == "codex" || id == "copilot") {
		return Provider{}, fmt.Errorf("%q is a signed-in agent account; pick another id", id)
	}
	p.ID = id

	if k := q.Get("key"); k != "" {
		if len(k) > maxImportKey {
			return Provider{}, errors.New("key is too long")
		}
		for _, r := range k {
			if unicode.IsControl(r) {
				return Provider{}, errors.New("key contains control characters")
			}
		}
		p.Key = strings.TrimSpace(k)
	}

	if m := q.Get("models"); m != "" {
		p.Models = cleanList(strings.Split(m, ","))
		if len(p.Models) > maxImportModels {
			p.Models = p.Models[:maxImportModels]
		}
	}
	if c := get("catalog"); c != "" {
		p.Catalog = c
	}
	if w := httpsOnly(get("website")); w != "" {
		p.Website = w
	}
	if k := httpsOnly(get("keys")); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxImportName {
		s = strings.TrimSpace(string(r[:maxImportName]))
	}
	return s
}

// httpsOnly returns a clean https URL, or "" for anything else.
func httpsOnly(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return s
}

// importBase validates one endpoint base URL.
func importBase(s string) (string, error) {
	if strings.ContainsAny(s, "?#") {
		return "", errors.New("a base URL cannot carry a query or fragment")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q", s)
	}
	if u.User != nil {
		return "", errors.New("a base URL cannot carry a username or password")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("invalid URL %q", s)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !localHost(u.Hostname()) {
			return "", errors.New("http is only allowed for local or private hosts; use https")
		}
	default:
		return "", fmt.Errorf("unsupported URL scheme %q; use https", u.Scheme)
	}
	return strings.TrimRight(s, "/"), nil
}

// localHost is true for this machine, loopback and private addresses,
// localhost and .local names.
func localHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
	}
	return false
}
