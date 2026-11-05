package provider

// Import links let a vendor or relay hand over a configured provider as one
// string: magpie://import?preset=deepseek&key=sk-…, or a custom provider with
// its own base URLs. Parsing only validates; nothing is saved and nothing is
// fetched here. The caller shows the result and saves it after the user agrees.

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links.
const Scheme = "magpie"

const (
	importMaxName   = 80   // runes; longer names are truncated
	importMaxKey    = 4096 // bytes
	importMaxModels = 200
	importReserved  = "magpie" // the gateway's own name for agents; not a provider id
)

// importCatalogRe is what a valid catalog value looks like: a models.dev
// provider id made of lowercase letters, digits, '-' and '_'. Anything else
// is ignored rather than rejected.
var importCatalogRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ParseImport turns an import link into the provider it describes. It does
// not save the provider or touch the network. Invalid links are rejected with
// an error; dangerous values are never silently corrected. The exceptions are
// the fields the link spec says to ignore: catalog, website and keys.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	i := strings.IndexByte(link, ':')
	if i < 0 || !strings.EqualFold(link[:i], Scheme) {
		return Provider{}, fmt.Errorf("not a %s:// import link", Scheme)
	}
	rest := link[i+1:]
	if strings.Contains(rest, "#") {
		return Provider{}, fmt.Errorf("import link must not contain a fragment (#)")
	}
	path, rawQuery, hasQuery := strings.Cut(rest, "?")

	action := strings.TrimRight(strings.TrimLeft(path, "/"), "/")
	if !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("%s link action must be import, not %q", Scheme, action)
	}
	if !hasQuery {
		return Provider{}, fmt.Errorf("import link has no parameters")
	}

	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Provider{}, fmt.Errorf("import link has malformed parameters: %v", err)
	}
	// Unknown parameters are ignored. A repeated parameter takes its first
	// value, which is what url.Values.Get returns.
	get := func(k string) string { return strings.TrimSpace(vals.Get(k)) }

	name := get("name")
	if hasControl(name) {
		return Provider{}, fmt.Errorf("name contains control characters")
	}
	if utf8.RuneCountInString(name) > importMaxName {
		name = strings.TrimSpace(string([]rune(name)[:importMaxName]))
	}

	// The key is checked before trimming: a newline or other control character
	// is a malformed link, not whitespace to strip.
	rawKey := vals.Get("key")
	if hasControl(rawKey) {
		return Provider{}, fmt.Errorf("key contains control characters")
	}
	key := strings.TrimSpace(rawKey)
	if len(key) > importMaxKey {
		return Provider{}, fmt.Errorf("key is longer than %d characters", importMaxKey)
	}

	models, err := importModels(vals.Get("models"))
	if err != nil {
		return Provider{}, err
	}

	var p Provider
	if preset := strings.ToLower(get("preset")); preset != "" {
		p, err = importFromPreset(preset, get("region"))
		if err != nil {
			return Provider{}, err
		}
		if name != "" {
			p.Name = name
		}
		p.ID = p.Preset
		if id := get("id"); id != "" {
			if p.ID, err = importID(id); err != nil {
				return Provider{}, err
			}
		}
	} else {
		// region only means something with a preset; without one it is ignored.
		if name == "" {
			return Provider{}, fmt.Errorf("import link needs either preset or name")
		}
		p = Provider{Name: name}
		if p.Chat, err = importBase("chat", get("chat")); err != nil {
			return Provider{}, err
		}
		if p.Responses, err = importBase("responses", get("responses")); err != nil {
			return Provider{}, err
		}
		if p.Anthropic, err = importBase("anthropic", get("anthropic")); err != nil {
			return Provider{}, err
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, fmt.Errorf("custom import link needs at least one base URL (chat, responses or anthropic)")
		}
		if c := get("catalog"); importCatalogRe.MatchString(c) {
			p.Catalog = c
		}
		p.Website = importHTTPSOnly(get("website"))
		p.KeysURL = importHTTPSOnly(get("keys"))
		idSource := name
		if id := get("id"); id != "" {
			idSource = id
		}
		if p.ID, err = importID(idSource); err != nil {
			return Provider{}, err
		}
	}
	p.Key = key
	p.Models = models
	return p, nil
}

// importFromPreset starts a provider from a preset and, when given, the
// region of that preset. Without a region the first (default) region is used.
func importFromPreset(id, region string) (Provider, error) {
	pr := Preset(id)
	if pr == nil {
		return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", id)
	}
	p, err := FromPreset(pr.ID)
	if err != nil {
		return Provider{}, err
	}
	if len(pr.Regions) == 0 {
		if region != "" {
			return Provider{}, fmt.Errorf("preset %q has no regions", pr.ID)
		}
		return p, nil
	}
	r := pr.Regions[0]
	if region != "" {
		found := false
		for _, cand := range pr.Regions {
			if strings.EqualFold(cand.ID, region) {
				r, found = cand, true
				break
			}
		}
		if !found {
			return Provider{}, fmt.Errorf("preset %q has no region %q", pr.ID, region)
		}
	}
	p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
	return p, nil
}

// importID normalises an id the way Slug does and rejects ids that come out
// empty or reserved.
func importID(raw string) (string, error) {
	id := Slug(raw)
	if id == "" {
		return "", fmt.Errorf("%q gives an empty provider id; pass id= with letters or digits", raw)
	}
	if id == importReserved {
		return "", fmt.Errorf("%q is a reserved provider id; pick another name or id", id)
	}
	return id, nil
}

// importBase validates one base URL. It must be https, except for hosts on
// this machine or the local network. It may not carry credentials, a query or
// a fragment. The trailing slash is removed.
func importBase(field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("%s URL must not contain a query or fragment", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s URL is not valid: %v", field, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !importLocalHost(u.Hostname()) {
			return "", fmt.Errorf("%s URL must be https (http is only allowed for localhost, loopback, private-network or .local hosts)", field)
		}
	default:
		return "", fmt.Errorf("%s URL must be https, not %q", field, u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s URL must not contain a username or password", field)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s URL has no host", field)
	}
	return strings.TrimRight(raw, "/"), nil
}

// importLocalHost reports whether a host is on this machine or the local
// network: localhost, a loopback or private IP address, or a .local name.
func importLocalHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// importHTTPSOnly keeps a website or keys URL only when it is an https URL
// with a host. Anything else is ignored.
func importHTTPSOnly(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return raw
}

// importModels splits the comma-separated model list. Empty items and
// surrounding spaces are dropped; duplicates are dropped too, as elsewhere.
func importModels(raw string) ([]string, error) {
	ms := cleanList(strings.Split(raw, ","))
	for _, m := range ms {
		if hasControl(m) {
			return nil, fmt.Errorf("model %q contains control characters", m)
		}
	}
	if len(ms) > importMaxModels {
		return nil, fmt.Errorf("import link lists %d models; the limit is %d", len(ms), importMaxModels)
	}
	return ms, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
