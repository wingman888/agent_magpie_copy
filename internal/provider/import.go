package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Scheme is the URL scheme of import links: magpie://import?…
const Scheme = "magpie"

const (
	maxImportName   = 80
	maxImportKey    = 4096
	maxImportModels = 200
)

// ParseImport turns a magpie://import link into a provider description. It
// has no side effects: nothing is written, fetched or logged. The link is
// untrusted, so the caller must show the result and ask before saving it.
func ParseImport(link string) (Provider, error) {
	rest := strings.TrimSpace(link)
	if len(rest) < len(Scheme)+1 || !strings.EqualFold(rest[:len(Scheme)+1], Scheme+":") {
		return Provider{}, errors.New("not a magpie:// link")
	}
	rest = rest[len(Scheme)+1:]
	rest = strings.TrimPrefix(rest, "//")
	rest = strings.TrimPrefix(rest, "/")
	action, query, _ := strings.Cut(rest, "?")
	if i := strings.IndexByte(query, '#'); i >= 0 {
		query = query[:i]
	}
	action = strings.TrimRight(action, "/")
	if !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("magpie://%s is not something magpie knows; links start magpie://import?", action)
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("bad link parameters: %w", err)
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	if id := get("preset"); id != "" {
		pr := Preset(strings.ToLower(id))
		if pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", id)
		}
		p, err = FromPreset(pr.ID)
		if err != nil {
			return Provider{}, err
		}
		if rid := get("region"); rid != "" {
			found := false
			for _, r := range pr.Regions {
				if strings.EqualFold(r.ID, rid) {
					p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
					found = true
					break
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("preset %s has no region %q", pr.ID, rid)
			}
		}
	} else {
		if get("name") == "" {
			return Provider{}, errors.New("a link without a preset needs a name")
		}
		p.Chat, p.Responses, p.Anthropic = get("chat"), get("responses"), get("anthropic")
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, errors.New("a link without a preset needs chat, responses or anthropic")
		}
		if c := strings.ToLower(get("catalog")); c != "" && c == Slug(c) {
			p.Catalog = c
			p.Icon = IconForCatalog(c)
		}
		p.Website = httpsOnly(get("website"))
		p.KeysURL = httpsOnly(get("keys"))
	}

	if name := get("name"); name != "" {
		p.Name = truncateBytes(name, maxImportName)
	}
	if id := get("id"); id != "" {
		p.ID = Slug(id)
	} else if p.Preset == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, errors.New("the link gives no usable provider id")
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	for _, e := range []*string{&p.Chat, &p.Responses, &p.Anthropic} {
		if *e == "" {
			continue
		}
		if *e, err = checkEndpoint(*e); err != nil {
			return Provider{}, err
		}
	}

	key := q.Get("key")
	if len(key) > maxImportKey {
		return Provider{}, errors.New("the key in the link is too long")
	}
	for _, r := range key {
		if r < ' ' || r == 0x7f {
			return Provider{}, errors.New("the key in the link has control characters")
		}
	}
	p.Key = key

	for _, m := range strings.Split(q.Get("models"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			if len(p.Models) == maxImportModels {
				break
			}
			p.Models = append(p.Models, m)
		}
	}
	return p, nil
}

// truncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// httpsOnly keeps a plain https URL (host, no userinfo) and drops anything else.
func httpsOnly(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return s
}

// checkEndpoint validates a base URL from a link and returns it without a
// trailing slash.
func checkEndpoint(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("bad endpoint %q", s)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && localHost(u.Hostname()):
	case u.Scheme == "http":
		return "", fmt.Errorf("endpoint %q must be https (http is only for local hosts)", s)
	default:
		return "", fmt.Errorf("endpoint %q must be an https URL", s)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("endpoint %q has no host", s)
	}
	if u.User != nil {
		return "", fmt.Errorf("endpoint %q must not carry a user name or password", s)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(s, "#") {
		return "", fmt.Errorf("endpoint %q must not have a query or fragment", s)
	}
	return strings.TrimRight(s, "/"), nil
}

func localHost(h string) bool {
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
