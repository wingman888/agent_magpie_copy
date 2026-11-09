package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// An import link is magpie://import?<params>, the way a vendor or relay
// hands out a provider to add in one click. ParseImport only reads it: the
// caller shows the result and saves it after the user agrees. Any invalid
// part rejects the whole link; nothing is partly imported.

const (
	importMaxModels = 200
	importMaxKey    = 4096
	importMaxName   = 80
)

// ParseImport turns a magpie://import link into a provider. It saves nothing.
func ParseImport(link string) (Provider, error) {
	query, err := importQuery(link)
	if err != nil {
		return Provider{}, err
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("import link: bad parameters: %v", err)
	}
	single := func(k string) (string, error) {
		xs := vals[k]
		switch len(xs) {
		case 0:
			return "", nil
		case 1:
			return xs[0], nil
		}
		return "", fmt.Errorf("import link: %q is given more than once", k)
	}

	presetID, err := single("preset")
	if err != nil {
		return Provider{}, err
	}
	regionID, err := single("region")
	if err != nil {
		return Provider{}, err
	}
	nameRaw, err := single("name")
	if err != nil {
		return Provider{}, err
	}
	idRaw, err := single("id")
	if err != nil {
		return Provider{}, err
	}
	keyRaw, err := single("key")
	if err != nil {
		return Provider{}, err
	}
	chatRaw, err := single("chat")
	if err != nil {
		return Provider{}, err
	}
	responsesRaw, err := single("responses")
	if err != nil {
		return Provider{}, err
	}
	anthropicRaw, err := single("anthropic")
	if err != nil {
		return Provider{}, err
	}
	catalogRaw, err := single("catalog")
	if err != nil {
		return Provider{}, err
	}
	websiteRaw, err := single("website")
	if err != nil {
		return Provider{}, err
	}
	keysRaw, err := single("keys")
	if err != nil {
		return Provider{}, err
	}

	// name
	name := strings.TrimSpace(nameRaw)
	if hasControl(name) {
		return Provider{}, errors.New("import link: name contains a control character")
	}
	if n := utf8.RuneCountInString(name); n > importMaxName {
		name = strings.TrimSpace(string([]rune(name)[:importMaxName]))
	}

	// the starting point: a preset, or nothing but what the link says
	var p Provider
	presetID = strings.TrimSpace(presetID)
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("import link: no preset %q — magpie presets lists them", presetID)
		}
		p, err = FromPreset(pr.ID)
		if err != nil {
			return Provider{}, err
		}
		if len(pr.Regions) > 0 {
			r := pr.Regions[0] // the first region is the default
			if want := strings.TrimSpace(regionID); want != "" {
				found := false
				for _, cand := range pr.Regions {
					if strings.EqualFold(cand.ID, want) {
						r, found = cand, true
						break
					}
				}
				if !found {
					return Provider{}, fmt.Errorf("import link: preset %q has no region %q", pr.ID, want)
				}
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		} else if strings.TrimSpace(regionID) != "" {
			return Provider{}, fmt.Errorf("import link: preset %q has no regions", pr.ID)
		}
		if name != "" {
			p.Name = name
		}
	} else {
		if name == "" {
			return Provider{}, errors.New("import link: name is required when no preset is given")
		}
		if strings.TrimSpace(regionID) != "" {
			return Provider{}, errors.New("import link: region needs a preset")
		}
		p = Provider{Name: name}
	}

	// id
	if strings.TrimSpace(idRaw) != "" {
		if Slug(idRaw) != idRaw {
			return Provider{}, fmt.Errorf("import link: id %q must be lowercase letters, digits and dashes (for example %q)", idRaw, Slug(idRaw))
		}
		p.ID = idRaw
	} else if presetID == "" {
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("import link: name %q has no letters or digits to make an id from; give id=", p.Name)
		}
	}
	if p.ID == "" || p.ID == "magpie" {
		return Provider{}, fmt.Errorf("import link: %q is not a usable provider id", p.ID)
	}

	// base URLs; an explicit one overrides what the preset gave
	bases := []struct {
		raw string
		dst *string
	}{{chatRaw, &p.Chat}, {responsesRaw, &p.Responses}, {anthropicRaw, &p.Anthropic}}
	for _, b := range bases {
		v := strings.TrimSpace(b.raw)
		if v == "" {
			continue
		}
		clean, err := importBase(v)
		if err != nil {
			return Provider{}, err
		}
		*b.dst = clean
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("import link: needs a base URL (chat, responses or anthropic)")
	}

	// key
	if hasControl(keyRaw) {
		return Provider{}, errors.New("import link: key contains a control character")
	}
	if len(keyRaw) > importMaxKey {
		return Provider{}, fmt.Errorf("import link: key is longer than %d characters", importMaxKey)
	}
	p.Key = strings.TrimSpace(keyRaw)

	// models
	var models []string
	for _, v := range vals["models"] {
		for _, m := range strings.Split(v, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			if hasControl(m) {
				return Provider{}, errors.New("import link: a model id contains a control character")
			}
			models = append(models, m)
		}
	}
	if len(models) > importMaxModels {
		return Provider{}, fmt.Errorf("import link: at most %d models, got %d", importMaxModels, len(models))
	}
	p.Models = cleanList(models)

	// catalog, website, keys
	if c := strings.TrimSpace(catalogRaw); c != "" {
		p.Catalog = c
	}
	if w := importHTTPS(websiteRaw); w != "" {
		p.Website = w
	}
	if k := importHTTPS(keysRaw); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

// importQuery checks the scheme and action and returns the raw query string.
func importQuery(link string) (string, error) {
	s := strings.TrimSpace(link)
	if len(s) < len("magpie:") || !strings.EqualFold(s[:len("magpie:")], "magpie:") {
		return "", errors.New("not a magpie link (it must start with magpie://)")
	}
	rest := s[len("magpie:"):]
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:] // magpie://import and magpie:///import
		rest = strings.TrimPrefix(rest, "/")
	}
	if strings.Contains(rest, "#") {
		return "", errors.New("import link must not have a fragment")
	}
	action, query, _ := strings.Cut(rest, "?")
	if action = strings.TrimSuffix(action, "/"); !strings.EqualFold(action, "import") {
		return "", fmt.Errorf("unsupported magpie link action %q (only import is accepted)", action)
	}
	return query, nil
}

// importBase checks a base URL and returns it without trailing slashes.
func importBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("import link: base URL %q is not a URL", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("import link: base URL %q needs a host", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !importLocalHost(u.Hostname()) {
			return "", fmt.Errorf("import link: base URL %q must be https (http is allowed only for this machine or a private network)", raw)
		}
	default:
		return "", fmt.Errorf("import link: base URL %q must be https", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("import link: base URL %q must not carry a user name or password", raw)
	}
	if strings.Contains(raw, "?") {
		return "", fmt.Errorf("import link: base URL %q must not have a query", raw)
	}
	if strings.Contains(raw, "#") || u.Fragment != "" {
		return "", fmt.Errorf("import link: base URL %q must not have a fragment", raw)
	}
	return scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// importLocalHost is true for hosts that never leave this machine or network.
func importLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	return false
}

// importHTTPS returns an https URL without trailing slashes, or "" for
// anything else: website and keys links that are not https are ignored.
func importHTTPS(raw string) string {
	v := strings.TrimSpace(raw)
	u, err := url.Parse(v)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Host == "" {
		return ""
	}
	return strings.TrimRight(v, "/")
}

// hasControl reports whether s holds any control character, newline included.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
