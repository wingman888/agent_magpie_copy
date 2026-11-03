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

// Import links (magpie://import?…) describe a provider a website or a relay
// wants the user to add with one click. ParseImport reads and checks one; it
// never saves anything. A single rule violation rejects the whole link: a
// partial import would be worse than none.

const (
	maxImportName   = 80   // runes; longer names are cut
	maxImportModels = 200  // non-empty entries in models=
	maxImportKey    = 4096 // runes
)

// ParseImport turns a magpie://import link into a Provider, validated the same
// way `magpie provider add` would validate it. It does not save anything.
//
// Error messages never include the link or the key: the key is a secret, and
// the link is the only place it arrives in.
func ParseImport(link string) (Provider, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "magpie" {
		// url.Parse errors quote the whole URL, key included; do not pass them on.
		return Provider{}, errors.New("not a magpie link")
	}
	if u.User != nil {
		return Provider{}, errors.New("magpie links may not carry a username or password")
	}
	// magpie://import?, magpie:import?, magpie:///import? and import/ all name
	// the same action; anything else (magpie://delete?…) is refused.
	action := u.Host + u.Path
	if u.Opaque != "" {
		action = u.Opaque
	}
	if !strings.EqualFold(strings.Trim(action, "/"), "import") {
		return Provider{}, fmt.Errorf("unsupported magpie action %q; only import is", strings.Trim(action, "/"))
	}

	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errors.New("malformed link parameters")
	}
	get := func(name string) (string, bool, error) {
		vs, ok := q[name]
		if !ok {
			return "", false, nil
		}
		if len(vs) > 1 {
			return "", false, fmt.Errorf("parameter %q given more than once", name)
		}
		return vs[0], true, nil
	}
	str := func(name string) (string, error) {
		v, _, err := get(name)
		return strings.TrimSpace(v), err
	}

	var p Provider
	presetID, err := str("preset")
	if err != nil {
		return Provider{}, err
	}
	presetID = strings.ToLower(presetID)
	region, err := str("region")
	if err != nil {
		return Provider{}, err
	}
	if presetID != "" {
		pr, err := FromPreset(presetID)
		if err != nil {
			return Provider{}, fmt.Errorf("unknown preset %q", presetID)
		}
		p = pr
		if region != "" {
			r, err := presetRegion(presetID, region)
			if err != nil {
				return Provider{}, err
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
	} else if region != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	name, err := str("name")
	if err != nil {
		return Provider{}, err
	}
	if name == "" && presetID == "" {
		return Provider{}, errors.New("a link without a preset needs a name")
	}
	if name != "" {
		name = strings.TrimSpace(truncateRunes(name, maxImportName))
		if Slug(name) == "" {
			return Provider{}, errors.New("name has no letters or digits to make an id from")
		}
		p.Name = name
	}

	id, err := str("id")
	if err != nil {
		return Provider{}, err
	}
	switch {
	case id != "":
		if id != Slug(id) {
			return Provider{}, fmt.Errorf("id %q must be lowercase letters, digits and dashes", id)
		}
		p.ID = id
	case presetID != "":
		// the preset's id stands
	default:
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, errors.New("no id could be derived; give id=")
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	for _, b := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		raw, err := str(b.param)
		if err != nil {
			return Provider{}, err
		}
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
		return Provider{}, errors.New("give a base URL: chat=, responses= or anthropic=")
	}

	if raw, ok, err := get("models"); err != nil {
		return Provider{}, err
	} else if ok {
		var ms []string
		for _, m := range strings.Split(raw, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, m)
			}
		}
		if len(ms) > maxImportModels {
			return Provider{}, fmt.Errorf("models: at most %d allowed", maxImportModels)
		}
		for _, m := range ms {
			if hasControl(m) {
				return Provider{}, errors.New("models: a model id contains a control character")
			}
		}
		p.Models = cleanList(ms)
	}

	if c, err := str("catalog"); err != nil {
		return Provider{}, err
	} else if c != "" {
		p.Catalog = c
	}

	// website and keys are advisory: anything but https is silently dropped.
	for _, f := range []struct {
		param string
		dst   *string
	}{{"website", &p.Website}, {"keys", &p.KeysURL}} {
		raw, err := str(f.param)
		if err != nil {
			return Provider{}, err
		}
		if s := httpsURL(raw); s != "" {
			*f.dst = s
		}
	}

	if raw, ok, err := get("key"); err != nil {
		return Provider{}, err
	} else if ok {
		if hasControl(raw) {
			return Provider{}, errors.New("key contains a control character (a line break?)")
		}
		if utf8.RuneCountInString(raw) > maxImportKey {
			return Provider{}, fmt.Errorf("key is longer than %d characters", maxImportKey)
		}
		p.Key = strings.TrimSpace(raw)
	}

	return p, nil
}

// presetRegion finds one of a preset's regional endpoint sets by id.
func presetRegion(presetID, region string) (Region, error) {
	pr := Preset(presetID)
	if pr == nil || len(pr.Regions) == 0 {
		return Region{}, fmt.Errorf("preset %q has no regions", presetID)
	}
	for _, r := range pr.Regions {
		if strings.EqualFold(r.ID, region) {
			return r, nil
		}
	}
	return Region{}, fmt.Errorf("unknown region %q for preset %q", region, presetID)
}

// importBase checks a base URL from a link and returns it without a trailing
// slash. https is required; plain http only for hosts on this machine or the
// local network. Userinfo, queries and fragments are refused.
func importBase(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.ContainsAny(base, "?#") {
		return "", errors.New("a base URL may not carry a query or fragment")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", errors.New("not a valid base URL")
	}
	if u.User != nil {
		return "", errors.New("a base URL may not carry a username or password")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return "", errors.New("http is only allowed for local or private hosts; use https")
		}
	default:
		return "", fmt.Errorf("scheme %q is not allowed; use https", u.Scheme)
	}
	return base, nil
}

// isLocalHost reports whether a host is this machine, a loopback or private
// address, localhost, or a .local name.
func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

// httpsURL returns s as a clean https URL, or "" when it is not one.
func httpsURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
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
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
