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

// An import link adds a provider in one click: magpie://import?preset=…&key=…
// or magpie://import?name=…&chat=…. ParseImport reads and checks a link. It
// saves nothing; the caller shows the result and saves it once the user agrees.
// A link with any problem is rejected as a whole.

const (
	importMaxModels = 200
	importMaxKey    = 4096
	importMaxName   = 80
)

// ParseImport turns a magpie:import link into a provider, or says why it
// cannot. Errors never repeat the key or the URL, so they are safe to print.
func ParseImport(link string) (Provider, error) {
	vals, err := importValues(link)
	if err != nil {
		return Provider{}, err
	}
	get := func(k string) string { return strings.TrimSpace(vals.Get(k)) }

	var p Provider
	presetID := strings.ToLower(get("preset"))
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
		p, _ = FromPreset(pr.ID)
		// a preset with regions defaults to the first; region picks another
		var region Region
		if len(pr.Regions) > 0 {
			region = pr.Regions[0]
		}
		if want := get("region"); want != "" {
			if len(pr.Regions) == 0 {
				return Provider{}, fmt.Errorf("preset %s has no regions", pr.ID)
			}
			found := false
			for _, x := range pr.Regions {
				if strings.EqualFold(x.ID, want) {
					region, found = x, true
					break
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, want)
			}
		}
		if len(pr.Regions) > 0 {
			p.Chat, p.Responses, p.Anthropic = region.Chat, region.Responses, region.Anthropic
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	name := get("name")
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Provider{}, errors.New("name contains a control character")
	}
	if name != "" {
		name = truncateRunes(name, importMaxName)
	}
	if presetID == "" && name == "" {
		return Provider{}, errors.New("a link without a preset needs a name")
	}
	if name != "" {
		p.Name = name
	}

	// the id: given, or the preset's, or derived from the name
	id := get("id")
	switch {
	case id != "":
		if id != Slug(id) {
			return Provider{}, fmt.Errorf("id %q must be lowercase letters, digits and dashes", id)
		}
	case presetID != "":
		id = p.ID
	default:
		id = Slug(name)
		if id == "" {
			return Provider{}, fmt.Errorf("name %q has no letters or digits to make an id from; pass id=", name)
		}
	}
	if id == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	p.ID = id

	// base URLs: a preset's are already https (or local); a link's override them
	bases := []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}}
	for _, b := range bases {
		if raw := get(b.param); raw != "" {
			clean, err := importBase(b.param, raw)
			if err != nil {
				return Provider{}, err
			}
			*b.dst = clean
		}
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a link needs a base URL: chat, responses or anthropic")
	}

	if raw := vals.Get("key"); raw != "" {
		if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return Provider{}, errors.New("key contains a control character")
		}
		if utf8.RuneCountInString(raw) > importMaxKey {
			return Provider{}, fmt.Errorf("key is longer than %d characters", importMaxKey)
		}
		if strings.TrimSpace(raw) != "" {
			p.Key = raw
		}
	}

	if raw := get("models"); vals.Has("models") {
		var ms []string
		for _, m := range strings.Split(raw, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, m)
			}
		}
		p.Models = cleanList(ms)
		if len(p.Models) > importMaxModels {
			return Provider{}, fmt.Errorf("a link lists at most %d models", importMaxModels)
		}
	}

	if c := get("catalog"); c != "" {
		p.Catalog = c
	}
	if w := get("website"); isHTTPSURL(w) {
		p.Website = w
	}
	if k := get("keys"); isHTTPSURL(k) {
		p.KeysURL = k
	}
	return p, nil
}

// importValues splits a magpie link into its parameters. It accepts
// magpie://import?…, magpie:import?…, magpie:///import?… and import/ with any
// letter case in the scheme.
func importValues(link string) (url.Values, error) {
	s := strings.TrimSpace(link)
	const scheme = "magpie:"
	if len(s) < len(scheme) || !strings.EqualFold(s[:len(scheme)], scheme) {
		return nil, errors.New("not a magpie link")
	}
	rest := s[len(scheme):]
	if strings.Contains(rest, "#") {
		return nil, errors.New("a magpie link has no fragment")
	}
	path, raw, _ := strings.Cut(rest, "?")
	action := strings.Trim(path, "/")
	if !strings.EqualFold(action, "import") {
		return nil, fmt.Errorf("magpie links can only import, not %q", action)
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("the link's parameters are malformed")
	}
	for k, v := range vals {
		if len(v) > 1 {
			return nil, fmt.Errorf("the link gives %q more than once", k)
		}
	}
	return vals, nil
}

// importBase checks one base URL. Only https is allowed, plus plain http for
// this machine, loopback, private networks and .local names.
func importBase(param, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a URL", param)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("%s must be an https URL", param)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s has no host", param)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s must not contain a username or password", param)
	}
	if u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "?") {
		return "", fmt.Errorf("%s must not contain a query string", param)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%s must not contain a fragment", param)
	}
	if u.Scheme == "http" && !localHost(u.Hostname()) {
		return "", fmt.Errorf("%s may use http only for localhost, a private address or a .local name", param)
	}
	return strings.TrimRight(raw, "/"), nil
}

// localHost is true for this machine, loopback, private networks and .local names.
func localHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// isHTTPSURL is true for an https URL with a host; anything else is ignored.
func isHTTPSURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
