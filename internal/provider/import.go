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

// An import link adds a provider from a URL, so a vendor or relay can offer a
// one-click setup:
//
//	magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-…
//
// ParseImport only reads the link. Nothing is saved; the caller shows the
// result and asks the user first.

const (
	maxImportModelsL3x = 200
	maxImportKeyL3x    = 4096
	maxImportNameL3x   = 80
)

// errBadLinkL3x is the only error text a bad link produces. Parser errors
// quote the link, and the link carries the key, so none of them are passed on.
var errBadLinkL3x = errors.New("not a valid magpie import link")

// ParseImport parses a magpie://import link into a provider. Any violation
// rejects the whole link: nothing is partly imported, and nothing is saved.
// Error messages never include the link or its key.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil {
		return Provider{}, errBadLinkL3x
	}
	if u.Scheme != "magpie" { // url.Parse lowercases the scheme
		return Provider{}, errors.New("not a magpie link")
	}
	// magpie://import?, magpie:import?, magpie:///import? and import/ all name
	// the same action; only the action after the scheme is looked at here.
	var action string
	switch {
	case u.Opaque != "":
		action = u.Opaque
	case u.Host != "":
		action = u.Host + u.Path
	default:
		action = u.Path
	}
	if !strings.EqualFold(strings.Trim(action, "/"), "import") {
		return Provider{}, errors.New("only magpie://import links are understood")
	}

	vals, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errBadLinkL3x
	}
	params := map[string]string{}
	for k, v := range vals {
		k = strings.ToLower(k)
		if _, dup := params[k]; dup || len(v) > 1 {
			return Provider{}, fmt.Errorf("parameter %q is given more than once", k)
		}
		if len(v) == 1 {
			params[k] = v[0]
		}
	}
	get := func(k string) string { return strings.TrimSpace(params[k]) }

	var p Provider
	if pid := strings.ToLower(get("preset")); pid != "" {
		pr := Preset(pid)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", pid)
		}
		p = Provider{
			ID: pr.ID, Name: pr.Name, Icon: pr.Icon, Preset: pr.ID,
			Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic,
			Catalog: pr.Catalog, Website: pr.Website, KeysURL: pr.KeysURL,
		}
		region := get("region")
		switch {
		case len(pr.Regions) > 0:
			r := pr.Regions[0] // the first region is the default
			if region != "" {
				found := false
				for _, x := range pr.Regions {
					if strings.EqualFold(x.ID, region) {
						r, found = x, true
						break
					}
				}
				if !found {
					return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, region)
				}
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		case region != "":
			return Provider{}, fmt.Errorf("preset %s has no regions", pr.ID)
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	if name := get("name"); name != "" {
		if strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return Provider{}, errors.New("name contains a control character")
		}
		p.Name = truncateRunesL3x(name, maxImportNameL3x)
	} else if p.Preset == "" {
		return Provider{}, errors.New("a link without a preset needs name=…")
	}

	for _, b := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		if raw := get(b.param); raw != "" {
			v, err := importBaseL3x(raw)
			if err != nil {
				return Provider{}, fmt.Errorf("%s: %w", b.param, err)
			}
			*b.dst = v
		}
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a link needs a base URL (chat, responses or anthropic)")
	}

	// id: given (and already in slug form), else the preset's, else from the name
	switch id := get("id"); {
	case id != "":
		if id != Slug(id) {
			return Provider{}, errors.New("id must be lowercase letters, digits and dashes")
		}
		p.ID = id
	case p.Preset != "":
		// keep the preset's id
	default:
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, errors.New("cannot derive an id from the name; give id=…")
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.Name == "" {
		p.Name = p.ID
	}

	models := cleanList(strings.Split(params["models"], ","))
	if len(models) > maxImportModelsL3x {
		return Provider{}, fmt.Errorf("at most %d models", maxImportModelsL3x)
	}
	p.Models = models

	if key := params["key"]; key != "" {
		if strings.IndexFunc(key, unicode.IsControl) >= 0 {
			return Provider{}, errors.New("key contains a control character")
		}
		key = strings.TrimSpace(key)
		if utf8.RuneCountInString(key) > maxImportKeyL3x {
			return Provider{}, fmt.Errorf("key is longer than %d characters", maxImportKeyL3x)
		}
		p.Key = key
	}

	if c := get("catalog"); c != "" {
		p.Catalog = c
	}
	if w := httpsLinkL3x(get("website")); w != "" {
		p.Website = w
	}
	if k := httpsLinkL3x(get("keys")); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

// importBaseL3x checks one base URL and returns it without a trailing slash.
func importBaseL3x(raw string) (string, error) {
	raw = strings.TrimRight(raw, "/")
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("must not contain a query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !localHostL3x(u.Hostname()) {
			return "", errors.New("http is only allowed for local or private hosts")
		}
	default:
		return "", errors.New("must be an https URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("needs a host")
	}
	if u.User != nil {
		return "", errors.New("must not contain a username or password")
	}
	return scheme + "://" + u.Host + u.EscapedPath(), nil
}

// localHostL3x is true for hosts an http base may use: this machine, loopback,
// private networks, localhost and .local names.
func localHostL3x(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

// httpsLinkL3x returns a website or keys page when it is an https URL, else "".
func httpsLinkL3x(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Host == "" {
		return ""
	}
	return raw
}

func truncateRunesL3x(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
