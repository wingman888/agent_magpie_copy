package provider

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links: magpie://import?<params>.
const Scheme = "magpie"

const (
	importMaxName   = 80   // runes; longer names are truncated
	importMaxModels = 200  // models per import link
	importMaxKey    = 4096 // bytes
)

// catalogIDRe is the shape of a models.dev provider id.
var catalogIDRe = regexp.MustCompile(`^[a-z0-9._-]+$`)

// ParseImport turns a magpie:// import link into the provider it would add.
// It only parses: it writes nothing, makes no network calls and reads no
// existing configuration. Saving is the caller's job, after the user confirms.
func ParseImport(link string) (Provider, error) {
	query, err := importQuery(link)
	if err != nil {
		return Provider{}, err
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("import link: malformed parameters: %v", err)
	}
	for k, v := range vals {
		if len(v) > 1 {
			return Provider{}, fmt.Errorf("import link: parameter %q given more than once", k)
		}
	}

	preset := strings.ToLower(strings.TrimSpace(vals.Get("preset")))
	region := strings.TrimSpace(vals.Get("region"))
	idArg := strings.TrimSpace(vals.Get("id"))

	// The key is checked before trimming so that a control character at
	// either end is rejected rather than silently removed.
	if err := importCheckKey(vals.Get("key")); err != nil {
		return Provider{}, err
	}
	key := strings.TrimSpace(vals.Get("key"))

	name, err := importName(vals.Get("name"))
	if err != nil {
		return Provider{}, err
	}

	models, err := importModels(vals.Get("models"))
	if err != nil {
		return Provider{}, err
	}

	var p Provider
	if preset != "" {
		p, err = FromPreset(preset)
		if err != nil {
			return Provider{}, fmt.Errorf("import link: %v", err)
		}
		if region != "" {
			pr := Preset(preset)
			r, ok := importRegion(pr.Regions, region)
			if !ok {
				return Provider{}, fmt.Errorf("import link: preset %q has no region %q", preset, region)
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
		if name != "" {
			p.Name = name
		}
	} else {
		if region != "" {
			return Provider{}, fmt.Errorf("import link: region %q needs a preset", region)
		}
		if name == "" {
			return Provider{}, fmt.Errorf("import link needs a preset or a name")
		}
		p = Provider{
			Name:    name,
			Catalog: importCatalog(vals.Get("catalog")),
			Website: importHTTPS(vals.Get("website")),
			KeysURL: importHTTPS(vals.Get("keys")),
		}
		if p.Chat, err = importBase("chat", vals.Get("chat")); err != nil {
			return Provider{}, err
		}
		if p.Responses, err = importBase("responses", vals.Get("responses")); err != nil {
			return Provider{}, err
		}
		if p.Anthropic, err = importBase("anthropic", vals.Get("anthropic")); err != nil {
			return Provider{}, err
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, fmt.Errorf("import link needs at least one base URL: chat, responses or anthropic")
		}
	}

	switch {
	case idArg != "":
		p.ID = Slug(idArg)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("import link: id %q has no letters or digits", idArg)
		}
	case preset == "":
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("import link: name %q has no letters or digits; add an id parameter", p.Name)
		}
	}
	if p.ID == "magpie" {
		return Provider{}, fmt.Errorf(`import link: "magpie" is reserved for the gateway itself; pick another id`)
	}

	p.Key = key
	p.Models = models
	return p, nil
}

// importQuery checks the scheme and action and returns the raw query string.
// Accepted spellings: magpie://import?, magpie:import?, magpie:///import?,
// MAGPIE://import/?  — the scheme and action are case-insensitive.
func importQuery(link string) (string, error) {
	s := strings.TrimSpace(link)
	if importHasControl(s) {
		return "", fmt.Errorf("import link contains a control character")
	}
	scheme, rest, ok := strings.Cut(s, ":")
	if !ok || !strings.EqualFold(scheme, Scheme) {
		return "", fmt.Errorf("not a %s:// link", Scheme)
	}
	if strings.Contains(rest, "#") {
		return "", fmt.Errorf("import link must not have a fragment")
	}
	rest = strings.TrimLeft(rest, "/")
	action, query, _ := strings.Cut(rest, "?")
	if !strings.EqualFold(strings.TrimRight(action, "/"), "import") {
		return "", fmt.Errorf("unsupported action %q; the only action is import", action)
	}
	return query, nil
}

// importCheckKey rejects control characters and oversized keys.
func importCheckKey(raw string) error {
	if importHasControl(raw) {
		return fmt.Errorf("import link: key contains a control character")
	}
	if len(raw) > importMaxKey {
		return fmt.Errorf("import link: key is longer than %d characters", importMaxKey)
	}
	return nil
}

// importName trims the name and truncates it to importMaxName runes.
func importName(raw string) (string, error) {
	if importHasControl(raw) {
		return "", fmt.Errorf("import link: name contains a control character")
	}
	s := strings.TrimSpace(raw)
	if utf8.RuneCountInString(s) > importMaxName {
		s = strings.TrimSpace(string([]rune(s)[:importMaxName]))
	}
	return s, nil
}

// importModels splits the comma-separated list, dropping empty items and
// duplicates, and enforces the model limit.
func importModels(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	items := strings.Split(raw, ",")
	for i, m := range items {
		// trim first, so surrounding spaces, tabs and newlines are plain
		// separators; only control characters left inside an item are errors
		m = strings.TrimSpace(m)
		if importHasControl(m) {
			return nil, fmt.Errorf("import link: model id contains a control character")
		}
		items[i] = m
	}
	models := cleanList(items)
	if len(models) > importMaxModels {
		return nil, fmt.Errorf("import link lists %d models; the limit is %d", len(models), importMaxModels)
	}
	return models, nil
}

// importBase validates a base URL. Only https is accepted, except for hosts
// on this machine or the local network. Credentials, queries, fragments and
// other schemes are rejected. The trailing slash is removed.
func importBase(field, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if importHasControl(s) {
		return "", fmt.Errorf("import link: %s URL contains a control character", field)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("import link: %s URL %q is not valid: %v", field, s, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !importLocalHost(strings.ToLower(u.Hostname())) {
			return "", fmt.Errorf("import link: %s URL %q must use https (http is allowed only for localhost, loopback, private network or .local hosts)", field, s)
		}
	default:
		return "", fmt.Errorf("import link: %s URL %q must use https", field, s)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("import link: %s URL %q has no host", field, s)
	}
	if u.User != nil {
		return "", fmt.Errorf("import link: %s URL must not contain a username or password", field)
	}
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(s, "?") {
		return "", fmt.Errorf("import link: %s URL must not contain a query", field)
	}
	if u.Fragment != "" || strings.Contains(s, "#") {
		return "", fmt.Errorf("import link: %s URL must not contain a fragment", field)
	}
	return strings.TrimRight(s, "/"), nil
}

// importLocalHost reports whether a lowercase host is on this machine or the
// local network, where plain http is acceptable.
func importLocalHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// importHTTPS returns the URL when it is an https link with a host, and ""
// otherwise. Website and keys-page fields are dropped silently when invalid.
func importHTTPS(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || importHasControl(s) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return ""
	}
	return s
}

// importCatalog returns a well-formed models.dev id, or "" to ignore it.
func importCatalog(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if catalogIDRe.MatchString(s) {
		return s
	}
	return ""
}

// importRegion finds a preset region by id, ignoring case.
func importRegion(rs []Region, id string) (Region, bool) {
	for _, r := range rs {
		if strings.EqualFold(r.ID, id) {
			return r, true
		}
	}
	return Region{}, false
}

// importHasControl reports whether s contains any control character,
// including newlines and tabs.
func importHasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
