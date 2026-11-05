package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links: magpie://import?preset=deepseek&key=…
const Scheme = "magpie"

const (
	importMaxName   = 80
	importMaxModels = 200
	importMaxKey    = 4096
)

var catalogRe = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

// ParseImport turns an import link into the provider it would add. It does
// not save anything, touch the disk, or reach the network: the caller shows
// the result to the user and saves it only after they confirm.
//
// Any link that fails validation is rejected with an error; nothing is
// silently repaired except the cosmetic cases the spec allows (trailing
// slashes, over-long names, excess models, unknown catalog ids, non-https
// website fields).
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	for _, r := range link {
		if r < 0x20 || r == 0x7f {
			return Provider{}, errors.New("import link contains control characters")
		}
	}
	if len(link) < len(Scheme)+1 || !strings.EqualFold(link[:len(Scheme)+1], Scheme+":") {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	rest := link[len(Scheme)+1:]
	path, rawQuery := rest, ""
	if i := strings.Index(rest, "?"); i >= 0 {
		path, rawQuery = rest[:i], rest[i+1:]
	}
	// "magpie:import", "magpie://import", "magpie:///import", "MAGPIE://import/"
	action := strings.Trim(path, "/")
	if !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("unsupported action %q; only import is allowed", action)
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed link parameters: %v", err)
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	// --- preset or custom endpoints ---
	presetID := get("preset")
	var pr *PresetDef
	if presetID != "" {
		pr = Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
	}

	// Check the raw value before trimming or truncating: control characters
	// must reject the link rather than be stripped or cut away silently.
	rawName := q.Get("name")
	for _, r := range rawName {
		if r < 0x20 || r == 0x7f {
			return Provider{}, errors.New("name contains control characters")
		}
	}
	name := strings.TrimSpace(rawName)
	if name != "" && utf8.RuneCountInString(name) > importMaxName {
		name = string([]rune(name)[:importMaxName])
		name = strings.TrimSpace(name)
	}
	if pr == nil && name == "" {
		return Provider{}, errors.New("an import link needs a preset or a name")
	}

	p := Provider{}
	if pr != nil {
		p.Preset = pr.ID
		p.ID = pr.ID
		p.Name = pr.Name
		p.Icon = pr.Icon
		p.Chat, p.Responses, p.Anthropic = pr.Chat, pr.Responses, pr.Anthropic
		p.Catalog = pr.Catalog
		p.Website = pr.Website
		p.KeysURL = pr.KeysURL
		reg := get("region")
		if len(pr.Regions) == 0 {
			if reg != "" {
				return Provider{}, fmt.Errorf("preset %q has no regions, so region %q does not exist", pr.ID, reg)
			}
		} else {
			if reg == "" {
				reg = pr.Regions[0].ID // the first region is the default
			}
			found := false
			for _, r := range pr.Regions {
				if r.ID == reg {
					p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
					found = true
					break
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("preset %q has no region %q", pr.ID, reg)
			}
		}
		if name != "" {
			p.Name = name
		}
		if id := get("id"); id != "" {
			p.ID = id
		}
	} else {
		p.Name = name
		p.Chat, err = importBaseURL(get("chat"), "chat")
		if err != nil {
			return Provider{}, err
		}
		p.Responses, err = importBaseURL(get("responses"), "responses")
		if err != nil {
			return Provider{}, err
		}
		p.Anthropic, err = importBaseURL(get("anthropic"), "anthropic")
		if err != nil {
			return Provider{}, err
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("a custom import link needs at least one of chat, responses or anthropic")
		}
		if c := get("catalog"); c != "" && len(c) <= 64 && catalogRe.MatchString(c) {
			p.Catalog = c
		}
		p.Website = importHTTPS(get("website"))
		p.KeysURL = importHTTPS(get("keys"))
		p.ID = get("id")
		if p.ID == "" {
			p.ID = Slug(p.Name)
		}
	}

	// id: normalise like Save does, then reject empty and reserved names.
	p.ID = Slug(p.ID)
	if p.ID == "" {
		return Provider{}, errors.New("the import link's id (or name) has no letters or digits")
	}
	if p.ID == Scheme {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.Name == "" {
		p.Name = p.ID
	}

	// key
	rawKey := q.Get("key")
	if len(rawKey) > importMaxKey {
		return Provider{}, fmt.Errorf("key is longer than %d characters", importMaxKey)
	}
	for _, r := range rawKey {
		if r < 0x20 || r == 0x7f {
			return Provider{}, errors.New("key contains control characters")
		}
	}
	p.Key = strings.TrimSpace(rawKey)

	// models
	for _, m := range strings.Split(q.Get("models"), ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		for _, r := range m {
			if r < 0x20 || r == 0x7f {
				return Provider{}, errors.New("a model id contains control characters")
			}
		}
		p.Models = append(p.Models, m)
	}
	p.Models = cleanList(p.Models)
	if len(p.Models) > importMaxModels {
		p.Models = p.Models[:importMaxModels]
	}

	return p, nil
}

// importBaseURL validates one base URL of a custom link and returns it with
// trailing slashes removed. Empty input yields "" with no error.
func importBaseURL(raw, field string) (string, error) {
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
		return "", fmt.Errorf("%s URL must not contain a username or password", field)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("%s URL must not contain a query or fragment", field)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("%s URL has no host", field)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLocalHost(host) {
			return "", fmt.Errorf("%s URL must use https (plain http is only allowed for localhost or LAN addresses)", field)
		}
	default:
		return "", fmt.Errorf("%s URL must use https, not %q", field, u.Scheme)
	}
	return strings.TrimRight(raw, "/"), nil
}

// isLocalHost reports whether host is one this machine or its LAN can reach
// directly: localhost, a loopback or private IP, or a .local name.
func isLocalHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return false
}

// importHTTPS keeps a website or key-page URL only when it is a well-formed
// https URL; anything else is ignored.
func importHTTPS(raw string) string {
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(raw, "/")
}
