package provider

// Import links: magpie://import?preset=…&key=…  turn a link into a provider
// description. ParseImport only parses and validates; it never saves anything.
// The caller shows the result to the user and saves it after confirmation.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	importMaxModels = 200
	importMaxName   = 80
	importMaxKey    = 4096
)

// accountIDs are the ids the signed-in agents (Accounts) are listed under.
var accountIDs = map[string]bool{"claude": true, "codex": true, "copilot": true}

// ParseImport turns a magpie://import link into a Provider. Any invalid part
// rejects the whole link. Nothing is written to disk.
func ParseImport(link string) (Provider, error) {
	q, err := importQuery(link)
	if err != nil {
		return Provider{}, err
	}

	var p Provider

	// preset / region
	preset := strings.ToLower(strings.TrimSpace(q.Get("preset")))
	region := strings.TrimSpace(q.Get("region"))
	var pr *PresetDef
	if preset != "" {
		pr = Preset(preset)
		if pr == nil {
			return Provider{}, fmt.Errorf("import: unknown preset %q — magpie presets lists them", preset)
		}
		p.Preset = pr.ID
		p.Icon = pr.Icon
		p.Catalog = pr.Catalog
		p.Website = pr.Website
		p.KeysURL = pr.KeysURL
		p.Chat, p.Responses, p.Anthropic = pr.Chat, pr.Responses, pr.Anthropic
	}
	if region != "" {
		if pr == nil {
			return Provider{}, errors.New("import: region needs a preset")
		}
		var found *Region
		for i := range pr.Regions {
			r := &pr.Regions[i]
			if strings.EqualFold(r.ID, region) || strings.EqualFold(r.Name, region) {
				found = r
				break
			}
		}
		if found == nil {
			return Provider{}, fmt.Errorf("import: preset %s has no region %q", pr.ID, region)
		}
		p.Chat, p.Responses, p.Anthropic = found.Chat, found.Responses, found.Anthropic
	} else if pr != nil && len(pr.Regions) > 0 {
		// a preset with regions defaults to the first one
		d := pr.Regions[0]
		p.Chat, p.Responses, p.Anthropic = d.Chat, d.Responses, d.Anthropic
	}

	// name
	name := q.Get("name")
	if hasControl(name) {
		return Provider{}, errors.New("import: name contains control characters")
	}
	name = truncateRunes(strings.TrimSpace(name), importMaxName)
	if pr != nil {
		if name == "" {
			name = pr.Name
		}
	} else if name == "" {
		return Provider{}, errors.New("import: a link without preset needs a name")
	}
	p.Name = name

	// id
	id := strings.TrimSpace(q.Get("id"))
	switch {
	case id != "":
		if id != Slug(id) {
			return Provider{}, fmt.Errorf("import: id %q must be lowercase letters, digits and dashes", id)
		}
	case pr != nil:
		id = pr.ID
	default:
		id = Slug(name)
	}
	if id == "" {
		return Provider{}, errors.New("import: the name has no usable characters for an id; give id=")
	}
	if id == "magpie" {
		return Provider{}, errors.New(`import: "magpie" is the gateway's own name; pick another id`)
	}
	if accountIDs[id] {
		// a signed-in agent is not a link-defined provider; reusing its id
		// would make the link save over the account's model picks
		return Provider{}, fmt.Errorf("import: %q is the id of a signed-in agent account; pick another id", id)
	}
	if pr == nil && Slug(name) == "" {
		return Provider{}, errors.New("import: the name has no usable characters")
	}
	p.ID = id

	// key
	rawKey := q.Get("key")
	if hasControl(rawKey) {
		return Provider{}, errors.New("import: key contains control characters")
	}
	if len(rawKey) > importMaxKey {
		return Provider{}, fmt.Errorf("import: key is longer than %d characters", importMaxKey)
	}
	p.Key = strings.TrimSpace(rawKey)

	// base URLs: an explicit parameter overrides the preset's endpoint
	for _, b := range []struct {
		param string
		dst   *string
	}{
		{"chat", &p.Chat},
		{"responses", &p.Responses},
		{"anthropic", &p.Anthropic},
	} {
		v := strings.TrimSpace(q.Get(b.param))
		if v == "" {
			continue
		}
		clean, err := checkBase(v)
		if err != nil {
			return Provider{}, fmt.Errorf("import: %s: %w", b.param, err)
		}
		*b.dst = clean
	}
	if pr == nil && p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("import: a link without preset needs at least one base URL (chat, responses or anthropic)")
	}

	// models
	var models []string
	for _, m := range strings.Split(q.Get("models"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}
	models = cleanList(models)
	if len(models) > importMaxModels {
		return Provider{}, fmt.Errorf("import: at most %d models", importMaxModels)
	}
	p.Models = models

	// catalog
	if c := strings.TrimSpace(q.Get("catalog")); c != "" {
		if hasControl(c) || strings.ContainsAny(c, " /?#") {
			return Provider{}, fmt.Errorf("import: catalog %q is not a models.dev provider id", c)
		}
		p.Catalog = c
	}

	// website / keys: https only, silently ignored otherwise
	if v := httpsOrEmpty(q.Get("website")); v != "" {
		p.Website = v
	}
	if v := httpsOrEmpty(q.Get("keys")); v != "" {
		p.KeysURL = v
	}

	return p, nil
}

// importQuery checks the scheme and action of a link and returns its query.
func importQuery(link string) (url.Values, error) {
	link = strings.TrimSpace(link)
	if hasControl(link) {
		return nil, errors.New("import: the link contains control characters")
	}
	if len(link) < len("magpie:") || !strings.EqualFold(link[:len("magpie:")], "magpie:") {
		return nil, errors.New("import: not a magpie link (expected magpie://import?…)")
	}
	rest := link[len("magpie:"):]
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		if strings.HasPrefix(rest, "/") {
			rest = rest[1:]
		}
	}
	if strings.Contains(rest, "#") {
		return nil, errors.New("import: a link must not have a fragment")
	}
	action, rawQuery, _ := strings.Cut(rest, "?")
	action = strings.TrimSuffix(action, "/")
	if !strings.EqualFold(action, "import") {
		return nil, fmt.Errorf("import: unsupported action %q (only import)", action)
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, errors.New("import: malformed query string")
	}
	return q, nil
}

// checkBase validates a base URL from a link and returns it without a trailing slash.
func checkBase(v string) (string, error) {
	if hasControl(v) || strings.ContainsAny(v, "?#") {
		return "", errors.New("base URL must not have a query or fragment")
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", errors.New("base URL is malformed")
	}
	if u.User != nil {
		return "", errors.New("base URL must not contain a username or password")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("base URL has no host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !localHost(host) {
			return "", fmt.Errorf("base URL must be https (http is only allowed for local or private hosts): %s", host)
		}
	default:
		return "", fmt.Errorf("base URL must be https, got scheme %q", u.Scheme)
	}
	return strings.TrimRight(v, "/"), nil
}

// localHost reports whether a host is this machine, loopback, private, or a
// .local name, where plain http is acceptable.
func localHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	return false
}

// httpsOrEmpty returns v as a clean https URL, or "" if it is not one.
func httpsOrEmpty(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || hasControl(v) {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(v, "/")
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}
