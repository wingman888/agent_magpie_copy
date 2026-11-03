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

// Import links: magpie://import?preset=…&key=… describe a provider the user
// may add. ParseImport only reads the link. It never saves, and it never
// touches the network: the caller shows what would be added and asks first.

const (
	importNameMax   = 80
	importKeyMax    = 4096
	importModelsMax = 200
)

// reservedIDs are the sign-in agents' own ids; a link must not claim them.
var reservedIDs = map[string]bool{"magpie": true, "claude": true, "codex": true, "copilot": true}

// ParseImport turns an import link into the provider it would add. Any
// invalid part rejects the whole link; no partial result is returned.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil {
		return Provider{}, errors.New("not a valid link") // url errors would echo the key
	}
	if !strings.EqualFold(u.Scheme, "magpie") {
		return Provider{}, errors.New("not a magpie link (expected magpie://import?…)")
	}
	if err := checkAction(u); err != nil {
		return Provider{}, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, errors.New("bad link parameters: check the percent-encoding")
	}
	for k, vs := range q {
		if len(vs) > 1 {
			return Provider{}, fmt.Errorf("parameter %q is given more than once", k)
		}
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	var regionEndpoints *Region
	presetID := strings.ToLower(get("preset"))
	if presetID != "" {
		pr := Preset(presetID)
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
		p = Provider{ID: pr.ID, Name: pr.Name, Icon: pr.Icon, Preset: pr.ID,
			Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic,
			Catalog: pr.Catalog, Website: pr.Website, KeysURL: pr.KeysURL}
		if len(pr.Regions) > 0 {
			// the first region is the default, as in the editor
			regionEndpoints = &pr.Regions[0]
		}
		if r := get("region"); r != "" {
			if len(pr.Regions) == 0 {
				return Provider{}, fmt.Errorf("%s has no regions", pr.Name)
			}
			found := false
			for i := range pr.Regions {
				if pr.Regions[i].ID == r {
					regionEndpoints, found = &pr.Regions[i], true
					break
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("%s has no region %q", pr.Name, r)
			}
		}
		if regionEndpoints != nil {
			p.Chat, p.Responses, p.Anthropic = regionEndpoints.Chat, regionEndpoints.Responses, regionEndpoints.Anthropic
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	// name
	name := get("name")
	if name != "" {
		if strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return Provider{}, errors.New("name contains control characters")
		}
		if utf8.RuneCountInString(name) > importNameMax {
			name = strings.TrimSpace(string([]rune(name)[:importNameMax]))
		}
	}
	if presetID == "" {
		if name == "" {
			return Provider{}, errors.New("a link without a preset needs a name")
		}
		p.Name = name
	} else if name != "" {
		p.Name = name
	}

	// base URLs: explicit ones override the preset's endpoints
	for _, b := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		if v := get(b.param); v != "" {
			clean, err := cleanBase(v)
			if err != nil {
				return Provider{}, fmt.Errorf("%s: %v", b.param, err)
			}
			*b.dst = clean
		}
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("a link without a preset needs at least one base URL (chat, responses or anthropic)")
	}

	// id
	if id := get("id"); id != "" {
		if Slug(id) != id {
			return Provider{}, fmt.Errorf("id %q must be lowercase letters, digits and dashes", id)
		}
		p.ID = id
	} else if presetID == "" {
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("name %q gives no usable id; pass id=", p.Name)
		}
	}
	if p.ID == "" || p.ID != Slug(p.ID) {
		return Provider{}, fmt.Errorf("provider id %q is not usable", p.ID)
	}
	if reservedIDs[p.ID] {
		return Provider{}, fmt.Errorf("id %q is reserved; pick another with id=", p.ID)
	}

	// key
	if raw := q.Get("key"); raw != "" {
		if utf8.RuneCountInString(raw) > importKeyMax {
			return Provider{}, fmt.Errorf("key is longer than %d characters", importKeyMax)
		}
		if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return Provider{}, errors.New("key contains control characters")
		}
		p.Key = strings.TrimSpace(raw) // blank after trimming means no key
	}

	// models
	if raw := q.Get("models"); raw != "" {
		var ms []string
		for _, m := range strings.Split(raw, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, m)
			}
		}
		ms = cleanList(ms)
		if len(ms) > importModelsMax {
			ms = ms[:importModelsMax]
		}
		p.Models = ms
	}

	if c := get("catalog"); c != "" {
		p.Catalog = c
	}
	// website and keys are only trusted over https; anything else is ignored
	if w := httpsOnly(get("website")); w != "" {
		p.Website = w
	}
	if k := httpsOnly(get("keys")); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

// checkAction accepts magpie://import, magpie:import, magpie:///import and
// the trailing-slash forms, and nothing else.
func checkAction(u *url.URL) error {
	var action, rest string
	switch {
	case u.Opaque != "": // magpie:import?…
		action = u.Opaque
	case u.Host != "": // magpie://import?… and magpie://import/?…
		action, rest = u.Host, u.Path
	default: // magpie:///import?…
		action = strings.TrimPrefix(u.Path, "/")
	}
	action = strings.TrimSuffix(action, "/")
	if rest != "" && rest != "/" {
		return errors.New("unexpected path in link")
	}
	if strings.ToLower(action) != "import" {
		return errors.New("unknown magpie action (only import is supported)")
	}
	return nil
}

// cleanBase validates a base URL and returns it without a trailing slash.
func cleanBase(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("base URL must not carry a query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("base URL is not valid")
	}
	if u.User != nil {
		return "", errors.New("base URL must not contain a username or password")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("base URL has no host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !localHost(host) {
			return "", errors.New("base URL must use https (http is only for this machine or a private network)")
		}
	default:
		return "", errors.New("base URL must use https")
	}
	return raw, nil
}

// localHost reports whether a host is this machine, a loopback or private
// address, localhost, or a .local name.
func localHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	return false
}

// httpsOnly returns the URL when it is a plausible https link, or "".
func httpsOnly(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}

// ImportHosts lists the hosts a provider's requests will be sent to, in the
// order the protocols are preferred, without repeats.
func (p Provider) ImportHosts() []string {
	var out []string
	for _, pr := range Protocols {
		if b := p.Base(pr); b != "" {
			if h := HostOf(b); !contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}
