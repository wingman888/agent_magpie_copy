package provider

// Import links: magpie://import?preset=…&key=… (or a custom vendor with
// name and base URLs). ParseImport only reads and checks a link; nothing is
// saved until the caller decides to.

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
	importNameMax   = 80   // runes; longer names are cut
	importKeyMax    = 4096 // runes
	importModelsMax = 200
)

// ParseImport turns a magpie://import link into a provider. Any invalid part
// rejects the whole link. Error messages never repeat the link, because it
// may carry an API key.
func ParseImport(link string) (Provider, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return Provider{}, errors.New("not a valid link")
	}
	if u.Scheme != "magpie" {
		return Provider{}, errors.New("not a magpie link (expected magpie://import?…)")
	}
	action := u.Opaque
	if action == "" && u.Host != "" {
		action = u.Host
		if strings.Trim(u.Path, "/") != "" {
			return Provider{}, errors.New("malformed magpie link")
		}
	}
	if action == "" {
		action = u.Path
	}
	if !strings.EqualFold(strings.Trim(action, "/"), "import") {
		return Provider{}, fmt.Errorf("unknown magpie action %q (only import is supported)", strings.Trim(action, "/"))
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errors.New("malformed link query")
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	presetID := strings.ToLower(get("preset"))
	if presetID != "" {
		fp, err := FromPreset(presetID)
		if err != nil {
			return Provider{}, fmt.Errorf("unknown preset %q — magpie presets lists them", presetID)
		}
		p = fp
		if region := get("region"); region != "" {
			r, ok := findRegion(Preset(presetID).Regions, region)
			if !ok {
				return Provider{}, fmt.Errorf("preset %s has no region %q", presetID, region)
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	name := strings.TrimSpace(truncateRunes(get("name"), importNameMax))
	if p.Preset == "" {
		if name == "" {
			return Provider{}, errors.New("a link without a preset needs a name")
		}
		p.Name = name
	} else if name != "" {
		p.Name = name
	}

	switch id := get("id"); {
	case id != "":
		if Slug(id) != id {
			return Provider{}, fmt.Errorf("provider id %q must be lowercase letters, digits and dashes", id)
		}
		p.ID = id
	case p.Preset != "":
		// keeps the preset's id
	default:
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, fmt.Errorf("name %q has no letters or digits to make an id from; add id=…", p.Name)
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	for _, b := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		raw := get(b.param)
		if raw == "" {
			continue
		}
		v, err := importBase(raw)
		if err != nil {
			return Provider{}, fmt.Errorf("%s: %w", b.param, err)
		}
		*b.dst = v
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a link needs a base URL (chat, responses or anthropic)")
	}

	// the key is checked raw: a percent-encoded newline must not slip past
	rawKey := q.Get("key")
	if utf8.RuneCountInString(rawKey) > importKeyMax {
		return Provider{}, errors.New("key is too long")
	}
	for _, r := range rawKey {
		if unicode.IsControl(r) {
			return Provider{}, errors.New("key contains a control character")
		}
	}
	p.Key = strings.TrimSpace(rawKey)

	// website and keys are optional extras: a non-https value is ignored
	if v := secureURL(get("website")); v != "" {
		p.Website = v
	}
	if v := secureURL(get("keys")); v != "" {
		p.KeysURL = v
	}

	if m := q.Get("models"); strings.TrimSpace(m) != "" {
		var list []string
		for _, x := range strings.Split(m, ",") {
			if x = strings.TrimSpace(x); x != "" {
				list = append(list, x)
			}
		}
		if len(list) > importModelsMax {
			return Provider{}, fmt.Errorf("at most %d models in a link", importModelsMax)
		}
		p.Models = cleanList(list)
	}

	if c := get("catalog"); c != "" {
		p.Catalog = c
	}

	if Slug(p.ID) != p.ID {
		return Provider{}, fmt.Errorf("provider id %q must be lowercase letters, digits and dashes", p.ID)
	}
	return p, nil
}

// importBase checks one base URL from a link and returns it without a
// trailing slash.
func importBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("must not contain a query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a valid URL")
	}
	if u.Host == "" {
		return "", errors.New("needs a host")
	}
	if u.User != nil {
		return "", errors.New("must not contain a username or password")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !localHost(u.Hostname()) {
			return "", errors.New("http is only allowed for localhost, private and .local addresses")
		}
	default:
		return "", errors.New("must be https")
	}
	return strings.TrimRight(raw, "/"), nil
}

// localHost reports whether a host is this machine, a private network
// address, or a .local name.
func localHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// secureURL returns raw when it is an https URL with a host, else "".
func secureURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return raw
}

func findRegion(rs []Region, id string) (Region, bool) {
	for _, r := range rs {
		if strings.EqualFold(r.ID, id) {
			return r, true
		}
	}
	return Region{}, false
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
