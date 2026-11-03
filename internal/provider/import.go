package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links: magpie://import?….
const Scheme = "magpie"

// Limits on what an import link may carry. Names and model lists are cut
// down to size; a key over the limit is refused rather than trimmed.
const (
	importMaxName   = 80
	importMaxModels = 200
	importMaxKey    = 4096
)

// importCatalogRe is the shape of a models.dev provider id.
var importCatalogRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport reads an import link and returns the provider it describes.
//
// It only parses and validates. It writes nothing and makes no network
// calls: saving is the caller's job, and only after the user has seen what
// the link would add.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return Provider{}, errors.New("empty import link")
	}
	if strings.Contains(link, "#") {
		return Provider{}, errors.New("an import link takes no fragment (#)")
	}
	u, err := url.Parse(link)
	if err != nil {
		return Provider{}, fmt.Errorf("not a valid link: %v", err)
	}
	if u.Scheme != Scheme {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	if u.User != nil {
		return Provider{}, errors.New("an import link must not carry a user name")
	}
	if act := importAction(u); act != "import" {
		return Provider{}, fmt.Errorf("unsupported action %q; the only one is import", act)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed parameters: %v", err)
	}

	// Refuse control characters anywhere in a value (a %0A in a key is a
	// line break, not something to quietly strip).
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range q[k] {
			if importHasControl(v) {
				return Provider{}, fmt.Errorf("parameter %s contains a control character", k)
			}
		}
	}

	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	presetID := strings.ToLower(get("preset"))
	regionID := get("region")
	name := truncateRunes(get("name"), importMaxName)
	idRaw := get("id")
	key := get("key")
	if len(key) > importMaxKey {
		return Provider{}, fmt.Errorf("key is longer than %d bytes", importMaxKey)
	}

	var p Provider
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
		p, _ = FromPreset(pr.ID)
		if regionID != "" {
			r, ok := importRegion(pr, regionID)
			if !ok {
				return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, regionID)
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
		// catalog, website and keys come from the preset; the link cannot
		// point them elsewhere.
		if name != "" {
			p.Name = name
		}
	} else {
		if regionID != "" {
			return Provider{}, errors.New("region only applies to a preset")
		}
		if name == "" {
			return Provider{}, errors.New("an import link needs a preset or a name")
		}
		p = Provider{Name: name}
		bases := []struct {
			field string
			dst   *string
		}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}}
		for _, b := range bases {
			v, err := importBase(b.field, get(b.field))
			if err != nil {
				return Provider{}, err
			}
			*b.dst = v
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("a custom link needs at least one of chat, responses or anthropic")
		}
		p.Catalog = importCatalog(get("catalog"))
		p.Website = importSite(get("website"))
		p.KeysURL = importSite(get("keys"))
	}

	var id string
	switch {
	case idRaw != "":
		id = Slug(idRaw)
	case presetID != "":
		id = p.ID
	default:
		id = Slug(p.Name)
	}
	if id == "" {
		return Provider{}, fmt.Errorf("cannot make a provider id from name %q — pass id=…", p.Name)
	}
	if id == Scheme {
		return Provider{}, fmt.Errorf("%q is what agents call the gateway itself; pick another id", Scheme)
	}
	p.ID = id

	p.Key = key
	p.Models = importModels(q.Get("models"))
	return p, nil
}

// importAction is the verb of a link: the part after the scheme, with the
// slashes that may surround it dropped. magpie://import?…, magpie:import?…,
// magpie:///import?… and MAGPIE://import/?… all give "import".
func importAction(u *url.URL) string {
	var a string
	switch {
	case u.Host != "":
		a = u.Host + u.Path
	case u.Opaque != "":
		a = u.Opaque
	default:
		a = u.Path
	}
	return strings.ToLower(strings.Trim(a, "/"))
}

// importBase checks one base URL of a custom link and returns it without a
// trailing slash. An empty value is no URL at all.
func importBase(field, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("%s: a base URL takes no query string or fragment", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: not a valid URL: %v", field, err)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s: a base URL must not carry a user name or password", field)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s: %q is not a full URL", field, raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !importLocalHost(u.Hostname()) {
			return "", fmt.Errorf("%s: plain http is only allowed for localhost, loopback, private-network or .local hosts", field)
		}
	default:
		return "", fmt.Errorf("%s: only https is allowed, not %q", field, u.Scheme+":")
	}
	return strings.TrimRight(raw, "/"), nil
}

// importLocalHost reports whether an http base may point here: the machine
// itself, a loopback or private address, or a .local name.
func importLocalHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || (strings.HasSuffix(h, ".local") && len(h) > len(".local")) {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// importSite keeps a website or key-page URL only when it is https; anything
// else is dropped.
func importSite(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

// importCatalog keeps a models.dev provider id that has a valid shape; any
// other value is ignored.
func importCatalog(raw string) string {
	c := strings.ToLower(strings.TrimSpace(raw))
	if !importCatalogRe.MatchString(c) {
		return ""
	}
	return c
}

// importModels splits the comma-separated model list, drops blanks and
// duplicates, and keeps at most importMaxModels entries.
func importModels(raw string) []string {
	if raw == "" {
		return nil
	}
	ms := cleanList(strings.Split(raw, ","))
	if len(ms) > importMaxModels {
		ms = ms[:importMaxModels]
	}
	return ms
}

// importRegion finds a preset region by id, ignoring case.
func importRegion(pr *PresetDef, id string) (Region, bool) {
	for _, r := range pr.Regions {
		if strings.EqualFold(r.ID, id) {
			return r, true
		}
	}
	return Region{}, false
}

// importHasControl reports whether s holds a control character or bytes that
// are not valid UTF-8.
func importHasControl(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// truncateRunes cuts s to at most n characters (not bytes).
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
