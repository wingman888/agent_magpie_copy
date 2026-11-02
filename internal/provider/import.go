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
	maxKeyLen    = 4096
	maxNameRunes = 80
	maxModels    = 200
)

var catalogRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ParseImport turns a magpie://import link into the provider it describes.
// It validates strictly and never saves anything or touches the network;
// whoever calls it must show the result to the user and get a yes first.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if len(link) <= len(Scheme) || !strings.EqualFold(link[:len(Scheme)+1], Scheme+":") {
		return Provider{}, errors.New("not a magpie:// link")
	}
	rest := link[len(Scheme)+1:]
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimPrefix(rest, "//")
	rest = strings.TrimPrefix(rest, "/")
	action, query, _ := strings.Cut(rest, "?")
	action = strings.TrimRight(action, "/")
	if !strings.EqualFold(action, "import") {
		if action == "" {
			action = "(none)"
		}
		return Provider{}, fmt.Errorf("unsupported magpie link action %q; only import is supported", action)
	}
	if query == "" {
		return Provider{}, errors.New("the import link has no parameters")
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %v", err)
	}
	get := func(k string) string { return strings.TrimSpace(vals.Get(k)) }

	var p Provider
	presetID := strings.ToLower(get("preset"))
	name := get("name")
	if presetID == "" && name == "" {
		return Provider{}, errors.New("the import link needs a preset or a name")
	}

	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
		p, _ = FromPreset(presetID)
		if rg := strings.ToLower(get("region")); rg != "" {
			found := false
			for _, r := range pr.Regions {
				if strings.ToLower(r.ID) == rg {
					p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
					found = true
					break
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("preset %s has no region %q", presetID, get("region"))
			}
		}
	} else {
		for _, e := range []struct {
			param string
			dst   *string
		}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
			if v := get(e.param); v != "" {
				u, err := cleanBase(e.param, v)
				if err != nil {
					return Provider{}, err
				}
				*e.dst = u
			}
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("a custom provider needs at least one of chat, responses or anthropic")
		}
		if c := get("catalog"); catalogRe.MatchString(c) {
			p.Catalog = c
		}
		p.Website = httpsOnly(get("website"))
		p.KeysURL = httpsOnly(get("keys"))
	}

	if name != "" {
		name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, name)
		if rs := []rune(name); len(rs) > maxNameRunes {
			name = strings.TrimSpace(string(rs[:maxNameRunes]))
		}
		p.Name = name
	}

	if v, ok := vals["id"]; ok && strings.TrimSpace(v[0]) != "" {
		p.ID = Slug(v[0])
		if p.ID == "" {
			return Provider{}, fmt.Errorf("provider id %q has no letters or digits", v[0])
		}
	} else if presetID == "" {
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("provider name %q has no letters or digits to derive an id from; add an id", p.Name)
		}
	}
	if p.ID == Scheme {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	if k, ok := vals["key"]; ok {
		key := k[0]
		if len(key) > maxKeyLen {
			return Provider{}, fmt.Errorf("the key is longer than %d characters", maxKeyLen)
		}
		for _, r := range key {
			if unicode.IsControl(r) {
				return Provider{}, errors.New("the key contains control characters")
			}
		}
		p.Key = strings.TrimSpace(key)
	}

	if m := vals.Get("models"); m != "" {
		p.Models = cleanList(strings.Split(m, ","))
		if len(p.Models) > maxModels {
			p.Models = p.Models[:maxModels]
		}
	}
	return p, nil
}

// cleanBase validates one base URL of an import link.
func cleanBase(param, raw string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%s URL %q rejected: %s", param, raw, why)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return bad("contains whitespace or control characters")
	}
	if strings.Contains(raw, "#") {
		return bad("must not have a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return bad("must be an https:// URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return bad("has no host")
	}
	if u.User != nil {
		return bad("must not contain a username or password")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return bad("must not have a query string")
	}
	if scheme == "http" && !isLocalHost(u.Hostname()) {
		return bad("http is only allowed for localhost and local-network hosts; use https")
	}
	out := scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
	return out, nil
}

// isLocalHost: localhost, loopback, private-range IPs and .local names.
func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

// httpsOnly keeps a website link only when it is a plain https URL.
func httpsOnly(raw string) string {
	if raw == "" || strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

// SaveImported saves a provider that came from an import link after the user
// confirmed it. Unlike Save it accepts a missing key: the user adds it later.
func SaveImported(p Provider) error { return save(p, false) }
