package provider

// Import links: magpie://import?preset=…&key=… lets a vendor or relay offer
// a provider the user can add with one click. ParseImport only reads the
// link; nothing is saved until the user confirms (see SaveImported).
//
// Errors from this file never repeat the link or any of its values: a link
// usually carries an API key, and errors end up in terminals and logs.

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
	importMaxName   = 80   // runes; longer names are cut
	importMaxKey    = 4096 // runes
	importMaxModels = 200
)

// importParams are the query parameters a link may carry. A repeated one
// makes the link ambiguous, so it is refused; unknown ones are ignored.
var importParams = []string{"preset", "region", "name", "id", "key", "chat", "responses", "anthropic", "models", "catalog", "website", "keys"}

// ParseImport reads a magpie://import link and returns the provider it
// describes. It saves nothing. An invalid link is refused as a whole.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	scheme, rest, ok := strings.Cut(link, ":")
	if !ok || !strings.EqualFold(scheme, "magpie") {
		return Provider{}, errors.New("not a magpie link; it should start with magpie://import?")
	}
	if strings.Contains(link, "#") {
		return Provider{}, errors.New("an import link has no fragment; put the parameters in the query")
	}
	u, err := url.Parse("magpie:" + rest)
	if err != nil {
		return Provider{}, errors.New("malformed import link")
	}
	if u.User != nil {
		return Provider{}, errors.New("an import link must not contain a username or password")
	}
	action := u.Opaque
	if action == "" {
		action = u.Host + u.Path
	}
	if !strings.EqualFold(strings.Trim(action, "/"), "import") {
		return Provider{}, fmt.Errorf("unknown magpie action %q; the only one is import", strings.Trim(action, "/"))
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errors.New("malformed import link: bad query string")
	}
	for _, k := range importParams {
		if len(q[k]) > 1 {
			return Provider{}, fmt.Errorf("parameter %q is given more than once", k)
		}
	}
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	return buildImport(get)
}

// buildImport checks the parameters of an import link and assembles the
// provider they describe.
func buildImport(get func(string) string) (Provider, error) {
	var p Provider
	var pr *PresetDef

	if presetID := strings.ToLower(strings.TrimSpace(get("preset"))); presetID != "" {
		preset, err := FromPreset(presetID)
		if err != nil {
			return Provider{}, err
		}
		p = preset
		pr = Preset(presetID)
	}

	if region := strings.TrimSpace(get("region")); region != "" {
		if pr == nil {
			return Provider{}, errors.New("region needs a preset")
		}
		found := false
		for _, rg := range pr.Regions {
			if strings.EqualFold(rg.ID, region) {
				p.Chat, p.Responses, p.Anthropic = rg.Chat, rg.Responses, rg.Anthropic
				found = true
				break
			}
		}
		if !found {
			return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, region)
		}
	}

	name := strings.TrimSpace(get("name"))
	if hasControl(name) {
		return Provider{}, errors.New("name contains control characters")
	}
	if utf8.RuneCountInString(name) > importMaxName {
		name = strings.TrimSpace(string([]rune(name)[:importMaxName]))
	}
	switch {
	case name != "":
		p.Name = name
	case pr != nil:
		p.Name = pr.Name
	default:
		return Provider{}, errors.New("a custom provider needs a name=")
	}

	rawID := strings.TrimSpace(get("id"))
	var id string
	switch {
	case rawID != "":
		if rawID != Slug(rawID) {
			return Provider{}, fmt.Errorf("provider id %q must be lowercase letters, digits and dashes", rawID)
		}
		id = rawID
	case pr != nil:
		id = pr.ID
	default:
		id = Slug(p.Name)
	}
	if id == "" {
		return Provider{}, fmt.Errorf("name %q has no letters or digits to make an id from; give id=", p.Name)
	}
	if id == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	p.ID = id
	p.Preset = ""
	if pr != nil {
		p.Preset = pr.ID
	}

	key := get("key")
	if hasControl(key) {
		return Provider{}, errors.New("key contains control characters")
	}
	if utf8.RuneCountInString(key) > importMaxKey {
		return Provider{}, fmt.Errorf("key is longer than %d characters", importMaxKey)
	}
	p.Key = strings.TrimSpace(key)

	for _, b := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		raw := strings.TrimSpace(get(b.param))
		if raw == "" {
			continue
		}
		base, err := importBase(raw)
		if err != nil {
			return Provider{}, fmt.Errorf("%s: %w", b.param, err)
		}
		*b.dst = base
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a link needs a base URL: chat=, responses= or anthropic=")
	}

	p.Models = importModels(get("models"))
	if c := strings.TrimSpace(get("catalog")); c != "" {
		p.Catalog = c
	}
	if w := importHTTPS(get("website")); w != "" {
		p.Website = w
	}
	if k := importHTTPS(get("keys")); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

// importBase checks one base URL from a link. https is required, except for
// local hosts. The result has no trailing slash.
func importBase(raw string) (string, error) {
	if strings.Contains(raw, "#") {
		return "", errors.New("must not have a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a valid URL")
	}
	if u.User != nil {
		return "", errors.New("must not contain a username or password")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("must not have a query string")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("needs a host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return "", errors.New("http is only allowed for localhost, loopback, private or .local hosts; use https")
		}
	default:
		return "", errors.New("must be an https URL")
	}
	return strings.TrimRight(raw, "/"), nil
}

// isLocalHost is true for hosts that never leave the user's machine or LAN.
func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return false
}

// importModels splits the comma-separated list: blanks and repeats are
// dropped, and at most importMaxModels are kept.
func importModels(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range strings.Split(s, ",") {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if len(out) == importMaxModels {
			break
		}
	}
	return out
}

// importHTTPS returns a website or key-page URL when it is https; anything
// else is ignored, as the link is not refused for it.
func importHTTPS(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
