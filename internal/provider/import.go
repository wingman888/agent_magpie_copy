package provider

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

// A magpie://import link describes a provider so that a vendor or relay can
// offer a one-click add. ParseImport only reads the link; nothing is saved
// until the user confirms (see Save and the import command).
//
// Errors deliberately never echo the link: it may carry an API key.

const (
	importAction   = "import"
	maxImportKey   = 4096
	maxImportName  = 80
	maxImportModel = 200
)

var errBadLink = errors.New("not a valid magpie import link")

// ParseImport turns a magpie:// import link into a provider. Any invalid
// parameter rejects the whole link; no partial result is returned.
func ParseImport(link string) (Provider, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		// url.Parse errors quote the URL, which would leak the key
		return Provider{}, errBadLink
	}
	if u.Scheme != "magpie" {
		return Provider{}, errors.New("not a magpie link")
	}
	// magpie://import, magpie:import, magpie:///import and import/ all name
	// the same action; the scheme itself is lowercased by url.Parse.
	action := u.Host + u.Path
	if u.Opaque != "" {
		action = u.Opaque
	}
	if strings.ToLower(strings.Trim(action, "/")) != importAction {
		return Provider{}, errors.New("magpie links only support import")
	}
	if u.User != nil {
		return Provider{}, errors.New("import links cannot carry a user name or password")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errBadLink
	}
	for k, v := range q {
		if len(v) > 1 {
			return Provider{}, errors.New("parameter " + k + " is given more than once")
		}
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	// --- preset or custom ----------------------------------------------
	var p Provider
	presetID := strings.ToLower(get("preset"))
	region := get("region")
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, errors.New("no preset " + presetID + " — magpie presets lists them")
		}
		base, err := FromPreset(presetID)
		if err != nil {
			return Provider{}, err
		}
		p = base
		if region != "" {
			found := false
			for _, r := range pr.Regions {
				if r.ID == region {
					p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
					found = true
					break
				}
			}
			if !found {
				return Provider{}, errors.New("preset " + presetID + " has no region " + region)
			}
		}
	} else {
		if region != "" {
			return Provider{}, errors.New("region only applies to a preset")
		}
		if get("name") == "" {
			return Provider{}, errors.New("an import without a preset needs a name")
		}
	}

	// --- name -----------------------------------------------------------
	if name := get("name"); name != "" {
		p.Name = truncateRunes(name, maxImportName)
	}
	if p.Name == "" && presetID == "" {
		return Provider{}, errors.New("an import needs a name")
	}

	// --- base URLs (explicit values override a preset's) -----------------
	for _, b := range []struct {
		key string
		dst *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		if v := get(b.key); v != "" {
			clean, err := importBase(v)
			if err != nil {
				return Provider{}, errors.New(b.key + ": " + err.Error())
			}
			*b.dst = clean
		}
	}
	if presetID == "" && p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("an import needs at least one base URL (chat, responses or anthropic)")
	}

	// --- id -------------------------------------------------------------
	id := get("id")
	switch {
	case id != "":
		if Slug(id) != id {
			return Provider{}, errors.New("id must be lowercase letters, digits and dashes")
		}
	case presetID != "":
		id = Preset(presetID).ID
	default:
		id = Slug(p.Name)
		if id == "" {
			return Provider{}, errors.New("the name has no letters or digits to make an id from; pass id=")
		}
	}
	if id == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	p.ID = id
	if p.Name == "" {
		p.Name = id
	}

	// --- key ------------------------------------------------------------
	if k, ok := q["key"]; ok && len(k) == 1 {
		key := k[0]
		if utf8.RuneCountInString(key) > maxImportKey {
			return Provider{}, errors.New("key is longer than 4096 characters")
		}
		for _, r := range key {
			if r < 0x20 || r == 0x7f {
				return Provider{}, errors.New("key contains a control character")
			}
		}
		p.Key = key
	}

	// --- models, catalog, links -------------------------------------------
	if raw := get("models"); raw != "" {
		var ms []string
		for _, m := range strings.Split(raw, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, m)
			}
		}
		if len(ms) > maxImportModel {
			return Provider{}, errors.New("at most 200 models can be listed")
		}
		p.Models = cleanList(ms)
	}
	if c := get("catalog"); c != "" {
		p.Catalog = c
	}
	// website and keys are only ever shown as links, so anything but https
	// is dropped silently rather than rejecting the whole import.
	if w := get("website"); isHTTPSLink(w) {
		p.Website = strings.TrimRight(w, "/")
	}
	if k := get("keys"); isHTTPSLink(k) {
		p.KeysURL = strings.TrimRight(k, "/")
	}
	return p, nil
}

// importBase validates one base URL from a link and returns it without a
// trailing slash.
func importBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a URL")
	}
	if u.User != nil {
		return "", errors.New("must not contain a user name or password")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("must not contain a query string")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("must not contain a fragment")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("must have a host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(host) {
			return "", errors.New("http is only allowed for localhost, loopback, private or .local hosts")
		}
	default:
		return "", errors.New("must be https")
	}
	return strings.TrimRight(raw, "/"), nil
}

// isLocalHost is true for hosts that never leave the machine or the LAN.
func isLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// isHTTPSLink reports whether s is an absolute https URL with a host.
func isHTTPSLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}
