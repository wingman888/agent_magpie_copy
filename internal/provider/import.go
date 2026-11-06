package provider

// Import links: magpie://import?… describes a provider to add. ParseImport
// only reads the link. It never writes to disk and never touches the network;
// the caller shows the result to the user and saves it only after they agree.

import (
	"errors"
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
	importMaxName   = 80
	importMaxModels = 200
	importMaxKey    = 4096
)

// catalogRe accepts models.dev provider ids such as "openai" or "fireworks-ai".
var catalogRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport turns an import link into a provider ready to be saved. It does
// not save anything and does not contact any host.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if len(link) < len(Scheme)+1 || !strings.EqualFold(link[:len(Scheme)+1], Scheme+":") {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	rest := link[len(Scheme)+1:]
	if strings.Contains(rest, "#") {
		return Provider{}, errors.New("import link must not contain a fragment")
	}
	// magpie:import, magpie://import, magpie:///import and MAGPIE://import/
	// all name the same action.
	rest = strings.TrimLeft(rest, "/")
	action, query, _ := strings.Cut(rest, "?")
	action = strings.TrimRight(action, "/")
	if !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("unsupported action %q; the only action is import", action)
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %v", err)
	}
	get := func(k string) (string, error) {
		xs := vals[k]
		if len(xs) > 1 {
			return "", fmt.Errorf("parameter %q given more than once", k)
		}
		if len(xs) == 0 {
			return "", nil
		}
		// raw value: the key is checked for control characters before any
		// trimming, so a trailing %0A is rejected rather than silently removed
		return xs[0], nil
	}
	var f struct{ preset, region, name, id, key, chat, responses, anthropic, models, catalog, website, keys string }
	for _, kv := range []struct {
		name string
		dst  *string
	}{
		{"preset", &f.preset}, {"region", &f.region}, {"name", &f.name}, {"id", &f.id},
		{"key", &f.key}, {"chat", &f.chat}, {"responses", &f.responses}, {"anthropic", &f.anthropic},
		{"models", &f.models}, {"catalog", &f.catalog}, {"website", &f.website}, {"keys", &f.keys},
	} {
		if *kv.dst, err = get(kv.name); err != nil {
			return Provider{}, err
		}
		if kv.name != "key" {
			*kv.dst = strings.TrimSpace(*kv.dst)
		}
	}

	if f.preset == "" && f.name == "" {
		return Provider{}, errors.New("import link needs a preset= or a name=")
	}
	if f.region != "" && f.preset == "" {
		return Provider{}, errors.New("region= only applies to a preset")
	}

	var p Provider
	if f.preset != "" {
		base, err := FromPreset(f.preset)
		if err != nil {
			return Provider{}, err
		}
		p = base
		if f.region != "" {
			pr := Preset(f.preset)
			var found *Region
			for i := range pr.Regions {
				if strings.EqualFold(pr.Regions[i].ID, f.region) {
					found = &pr.Regions[i]
					break
				}
			}
			if found == nil {
				return Provider{}, fmt.Errorf("preset %q has no region %q", f.preset, f.region)
			}
			p.Chat, p.Responses, p.Anthropic = found.Chat, found.Responses, found.Anthropic
		}
		if f.name != "" {
			p.Name = truncateRunes(f.name, importMaxName)
		}
		p.ID = p.Preset
		if f.id != "" {
			p.ID = Slug(f.id)
		}
	} else {
		p.Name = truncateRunes(f.name, importMaxName)
		p.ID = Slug(p.Name)
		if f.id != "" {
			p.ID = Slug(f.id)
		}
	}

	if p.ID == "" {
		return Provider{}, errors.New("provider id is empty; give id= or a name with letters or digits")
	}
	if p.ID == Scheme {
		return Provider{}, fmt.Errorf("%q is reserved for the gateway itself; pick another id or name", p.ID)
	}
	if p.Name == "" {
		p.Name = p.ID
	}

	if f.preset == "" {
		if f.chat == "" && f.responses == "" && f.anthropic == "" {
			return Provider{}, errors.New("import link needs at least one of chat=, responses=, anthropic=")
		}
		if p.Chat, err = importBase("chat", f.chat); err != nil {
			return Provider{}, err
		}
		if p.Responses, err = importBase("responses", f.responses); err != nil {
			return Provider{}, err
		}
		if p.Anthropic, err = importBase("anthropic", f.anthropic); err != nil {
			return Provider{}, err
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("import link needs at least one base URL")
		}
		if c := strings.ToLower(f.catalog); catalogRe.MatchString(c) {
			p.Catalog = c
		}
		p.Website = importSite(f.website)
		p.KeysURL = importSite(f.keys)
	}

	if f.key != "" {
		if len(f.key) > importMaxKey {
			return Provider{}, fmt.Errorf("key is longer than %d characters", importMaxKey)
		}
		for _, r := range f.key {
			if unicode.IsControl(r) {
				return Provider{}, errors.New("key contains a control character")
			}
		}
		p.Key = f.key
	}

	if f.models != "" {
		var models []string
		for _, m := range strings.Split(f.models, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			for _, r := range m {
				if unicode.IsControl(r) {
					return Provider{}, errors.New("model id contains a control character")
				}
			}
			models = append(models, m)
		}
		models = cleanList(models)
		if len(models) > importMaxModels {
			models = models[:importMaxModels]
		}
		p.Models = models
	}

	return p, nil
}

// importBase validates one base URL of a custom link and returns it without a
// trailing slash. An empty input yields "".
func importBase(field, raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("%s URL must not contain a query or fragment", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s URL is malformed: %v", field, err)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s URL must not contain credentials", field)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s URL needs a host", field)
	}
	host := strings.ToLower(u.Hostname())
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLocalHost(host) {
			return "", fmt.Errorf("%s URL must use https (http is only allowed for localhost and private addresses)", field)
		}
	default:
		return "", fmt.Errorf("%s URL must use https", field)
	}
	return strings.ToLower(u.Scheme) + raw[len(u.Scheme):], nil
}

// importSite keeps a website or key-page URL only when it is https; anything
// else is dropped without an error.
func importSite(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || strings.ToLower(u.Scheme) != "https" {
		return ""
	}
	return strings.ToLower(u.Scheme) + raw[len(u.Scheme):]
}

// isLocalHost reports whether plain http is acceptable for the host:
// localhost, loopback and private-network addresses, and .local names.
func isLocalHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
