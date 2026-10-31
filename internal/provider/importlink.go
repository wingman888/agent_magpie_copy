package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Scheme is the URL scheme of import links: magpie://import?…
const Scheme = "magpie"

const (
	maxImportKey    = 4096
	maxImportName   = 80
	maxImportModels = 200
)

var catalogIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport turns a magpie://import link into the provider it describes.
// It is pure: nothing is saved and nothing is fetched. Dangerous or
// malformed input is rejected, never quietly repaired.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if len(link) < len(Scheme)+1 || !strings.EqualFold(link[:len(Scheme)+1], Scheme+":") {
		return Provider{}, fmt.Errorf("not a %s: link", Scheme)
	}
	rest := link[len(Scheme)+1:]
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimLeft(rest, "/")
	action, query, _ := strings.Cut(rest, "?")
	action = strings.ToLower(strings.TrimRight(action, "/"))
	if action != "import" {
		return Provider{}, fmt.Errorf("unsupported link action %q (only import is supported)", action)
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %v", err)
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	presetID := strings.ToLower(get("preset"))
	name := get("name")
	if presetID == "" && name == "" {
		return Provider{}, errors.New("import link needs a preset or a name")
	}
	if hasControl(name) {
		return Provider{}, errors.New("provider name contains control characters")
	}
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("unknown preset %q — magpie presets lists them", presetID)
		}
		p, _ = FromPreset(presetID)
		if region := get("region"); region != "" {
			var found *Region
			for i := range pr.Regions {
				if strings.EqualFold(pr.Regions[i].ID, region) {
					found = &pr.Regions[i]
				}
			}
			if found == nil {
				return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, region)
			}
			p.Chat, p.Responses, p.Anthropic = found.Chat, found.Responses, found.Anthropic
		}
	} else {
		for _, f := range []struct {
			key string
			dst *string
		}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
			v := get(f.key)
			if v == "" {
				continue
			}
			u, err := cleanBaseURL(v)
			if err != nil {
				return Provider{}, fmt.Errorf("%s URL: %v", f.key, err)
			}
			*f.dst = u
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("import link needs at least one of chat, responses or anthropic")
		}
		if c := get("catalog"); catalogIDRe.MatchString(c) {
			p.Catalog = c
		}
		for _, f := range []struct {
			key string
			dst *string
		}{{"website", &p.Website}, {"keys", &p.KeysURL}} {
			if u, err := cleanBaseURL(get(f.key)); err == nil && strings.HasPrefix(u, "https://") {
				*f.dst = u
			}
		}
	}

	if name != "" {
		if r := []rune(name); len(r) > maxImportName {
			name = strings.TrimSpace(string(r[:maxImportName]))
		}
		p.Name = name
	}

	if id := get("id"); id != "" {
		p.ID = Slug(id)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("provider id %q has no letters or digits", id)
		}
	} else if presetID == "" {
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("cannot derive a provider id from the name %q; give an id", p.Name)
		}
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	if key := q.Get("key"); key != "" {
		if len(key) > maxImportKey {
			return Provider{}, fmt.Errorf("key is too long (over %d bytes)", maxImportKey)
		}
		if hasControl(key) {
			return Provider{}, errors.New("key contains control characters")
		}
		p.Key = strings.TrimSpace(key)
	}

	if m := q.Get("models"); m != "" {
		if hasControl(m) {
			return Provider{}, errors.New("models contain control characters")
		}
		p.Models = cleanList(strings.Split(m, ","))
		if len(p.Models) > maxImportModels {
			p.Models = p.Models[:maxImportModels]
		}
	}
	return p, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// cleanBaseURL validates an endpoint taken from a link and strips a trailing "/".
func cleanBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty URL")
	}
	if hasControl(raw) || strings.ContainsAny(raw, " \\") {
		return "", fmt.Errorf("%q is not a valid URL", raw)
	}
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("%q must not have a query or fragment", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid URL", raw)
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", fmt.Errorf("%q must not contain a username or password", raw)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return "", fmt.Errorf("%q must use https (http is only allowed for localhost and local networks)", raw)
		}
	default:
		return "", fmt.Errorf("%q must be an https URL", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
