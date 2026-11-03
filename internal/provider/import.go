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
	maxImportName   = 80   // runes; longer names are truncated
	maxImportKey    = 4096 // bytes
	maxImportModels = 200
)

// catalogRe matches models.dev provider ids such as "openai" or "fireworks-ai".
var catalogRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport turns an import link into the provider it would add. It saves
// nothing and makes no network requests: the caller shows the result to the
// user and only then calls Save. Every rule violation is an error.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	rest, ok := cutScheme(link)
	if !ok {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	action, query, _ := strings.Cut(rest, "?")
	// accepts magpie://import, magpie:import, magpie:///import, MAGPIE://import/
	action = strings.TrimRight(strings.TrimLeft(action, "/"), "/")
	if !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("unsupported action %q: the only action is import", action)
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed parameters: %v", err)
	}
	// Unknown parameters are ignored, and a repeated parameter takes its first
	// value (url.Values.Get semantics).
	get := func(k string) string { return strings.TrimSpace(vals.Get(k)) }

	var p Provider
	presetID := get("preset")
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
		base, err := FromPreset(pr.ID)
		if err != nil {
			return Provider{}, err
		}
		p = base
		if region := get("region"); region != "" {
			r := presetRegion(pr, region)
			if r == nil {
				return Provider{}, fmt.Errorf("preset %q has no region %q", pr.ID, region)
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	if name := get("name"); name != "" {
		if hasControl(name) {
			return Provider{}, errors.New("name contains control characters")
		}
		p.Name = truncateRunes(name, maxImportName)
	}
	if presetID == "" && p.Name == "" {
		return Provider{}, errors.New("an import link needs a preset or a name")
	}

	switch id := get("id"); {
	case id != "":
		if hasControl(id) {
			return Provider{}, errors.New("id contains control characters")
		}
		p.ID = Slug(id)
	case presetID != "":
		p.ID = p.Preset
	default:
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, fmt.Errorf("id is empty after normalizing; give an id with letters or digits")
	}
	if p.ID == Scheme {
		return Provider{}, fmt.Errorf("%q is reserved; pick another id", Scheme)
	}

	if key := vals.Get("key"); key != "" {
		if len(key) > maxImportKey {
			return Provider{}, fmt.Errorf("key is longer than %d bytes", maxImportKey)
		}
		for i := 0; i < len(key); i++ {
			// ASCII control characters (incl. CR/LF) and DEL; other bytes, such as
			// spaces and UTF-8 sequences, are kept as they are.
			if c := key[i]; c < 0x20 || c == 0x7f {
				return Provider{}, errors.New("key contains control characters")
			}
		}
		p.Key = key
	}

	if raw := vals.Get("models"); raw != "" {
		if hasControl(raw) {
			return Provider{}, errors.New("models contains control characters")
		}
		list := cleanList(strings.Split(raw, ","))
		if len(list) > maxImportModels {
			return Provider{}, fmt.Errorf("at most %d models per import, got %d", maxImportModels, len(list))
		}
		p.Models = list
	}

	if presetID != "" {
		// endpoints, catalog and links come from the preset; the rest is ignored
		return p, nil
	}

	var err2 error
	if p.Chat, err2 = checkBaseURL("chat", vals.Get("chat")); err2 != nil {
		return Provider{}, err2
	}
	if p.Responses, err2 = checkBaseURL("responses", vals.Get("responses")); err2 != nil {
		return Provider{}, err2
	}
	if p.Anthropic, err2 = checkBaseURL("anthropic", vals.Get("anthropic")); err2 != nil {
		return Provider{}, err2
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a custom import needs at least one base URL: chat, responses or anthropic")
	}
	if c := get("catalog"); catalogRe.MatchString(c) {
		p.Catalog = c
	}
	p.Website = httpsLink(get("website"))
	p.KeysURL = httpsLink(get("keys"))
	return p, nil
}

// cutScheme strips a case-insensitive "magpie:" prefix.
func cutScheme(link string) (string, bool) {
	n := len(Scheme) + 1
	if len(link) < n || !strings.EqualFold(link[:n], Scheme+":") {
		return "", false
	}
	return link[n:], true
}

func presetRegion(pr *PresetDef, id string) *Region {
	for i := range pr.Regions {
		if pr.Regions[i].ID == id {
			return &pr.Regions[i]
		}
	}
	return nil
}

// checkBaseURL validates a base URL and returns it without trailing slashes.
// An empty input is allowed and means "not offered".
func checkBaseURL(field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if hasControl(raw) {
		return "", fmt.Errorf("%s URL contains control characters", field)
	}
	s := strings.TrimRight(raw, "/")
	if strings.ContainsAny(s, "?#") {
		return "", fmt.Errorf("%s URL must not carry a query string or fragment", field)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%s URL is malformed: %v", field, err)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s URL must not contain a user name or password", field)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("%s URL has no host", field)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !localHost(host) {
			return "", fmt.Errorf("%s URL must use https; plain http is only allowed for localhost, loopback, private-network and .local hosts", field)
		}
	default:
		return "", fmt.Errorf("%s URL scheme %q is not allowed; use https", field, u.Scheme)
	}
	return s, nil
}

// localHost reports whether a host stays on this machine or the LAN.
func localHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// httpsLink keeps a display link only when it is a plain https URL; anything
// else is dropped.
func httpsLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || hasControl(raw) {
		return ""
	}
	s := strings.TrimRight(raw, "/")
	u, err := url.Parse(s)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return s
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
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return strings.TrimSpace(string(r))
}
