package provider

// Import links: magpie://import?name=…&chat=…&key=… describes a provider a
// vendor or relay wants to hand to its users. Parsing never saves anything;
// the caller shows the result and asks the user first.

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

const (
	importMaxName   = 80
	importMaxKey    = 4096
	importMaxModels = 200
)

var importCatalogRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// IsImportLink reports whether s looks like a magpie: link (any case).
func IsImportLink(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 7 && strings.EqualFold(s[:7], "magpie:")
}

// ParseImport turns an import link into the provider it describes. It
// validates strictly and returns an error for anything doubtful; nothing is
// saved.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if !IsImportLink(link) {
		return Provider{}, errors.New("not a magpie link")
	}
	rest := strings.TrimLeft(link[len("magpie:"):], "/")
	if len(rest) < 6 || !strings.EqualFold(rest[:6], "import") {
		return Provider{}, errors.New("unsupported magpie link: only import is allowed")
	}
	rest = rest[6:]
	switch {
	case rest == "":
	case rest[0] == '?':
	case rest[0] == '/':
		rest = strings.TrimPrefix(rest, "/")
		if rest != "" && rest[0] != '?' {
			return Provider{}, errors.New("unsupported magpie link: only import is allowed")
		}
	default:
		return Provider{}, errors.New("unsupported magpie link: only import is allowed")
	}
	rest = strings.TrimPrefix(rest, "?")
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	q, err := url.ParseQuery(rest)
	if err != nil {
		return Provider{}, fmt.Errorf("bad import link: %w", err)
	}
	get := func(k string) string { return q.Get(k) }

	var p Provider
	if id := strings.TrimSpace(get("preset")); id != "" {
		pr, err := FromPreset(strings.ToLower(id))
		if err != nil {
			return Provider{}, err
		}
		p = pr
		if rid := strings.TrimSpace(get("region")); rid != "" {
			def := Preset(p.Preset)
			found := false
			for _, r := range def.Regions {
				if r.ID == rid {
					p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
					found = true
				}
			}
			if !found {
				return Provider{}, fmt.Errorf("preset %s has no region %q", def.ID, rid)
			}
		}
	} else if get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}

	name := strings.TrimSpace(get("name"))
	if hasControl(name) {
		return Provider{}, errors.New("name has control characters")
	}
	if utf8.RuneCountInString(name) > importMaxName {
		name = strings.TrimSpace(string([]rune(name)[:importMaxName]))
	}
	if p.Preset == "" && name == "" {
		return Provider{}, errors.New("an import link without a preset needs a name")
	}
	if name != "" {
		p.Name = name
	}

	for _, b := range []struct {
		key string
		dst *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		if v := strings.TrimSpace(get(b.key)); v != "" {
			u, err := importBase(b.key, v)
			if err != nil {
				return Provider{}, err
			}
			*b.dst = u
		}
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("an import link needs at least one base URL (chat, responses or anthropic)")
	}

	if id := get("id"); id != "" {
		if id != Slug(id) || id == "" {
			return Provider{}, fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", id)
		}
		p.ID = id
	} else if p.Preset == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, errors.New("cannot derive a provider id from that name; add id=…")
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}

	if key := get("key"); key != "" {
		if hasControl(key) {
			return Provider{}, errors.New("key has control characters")
		}
		if utf8.RuneCountInString(key) > importMaxKey {
			return Provider{}, errors.New("key is too long")
		}
		p.Key = key
	}

	if m := get("models"); m != "" {
		var ms []string
		for _, x := range strings.Split(m, ",") {
			x = strings.TrimSpace(x)
			if x == "" {
				continue
			}
			if hasControl(x) {
				return Provider{}, errors.New("model id has control characters")
			}
			if !contains(ms, x) {
				ms = append(ms, x)
			}
		}
		if len(ms) > importMaxModels {
			ms = ms[:importMaxModels]
		}
		p.Models = ms
	}
	if c := strings.TrimSpace(get("catalog")); c != "" {
		if !importCatalogRe.MatchString(c) {
			return Provider{}, fmt.Errorf("bad catalog id %q", c)
		}
		p.Catalog = c
	}
	if w := httpsOnly(get("website")); w != "" {
		p.Website = w
	}
	if k := httpsOnly(get("keys")); k != "" {
		p.KeysURL = k
	}
	return p, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// httpsOnly returns a clean https URL, or "" for anything else.
func httpsOnly(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || hasControl(raw) || strings.ContainsAny(raw, " \t") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

// importBase validates a protocol base URL from a link.
func importBase(what, raw string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%s URL %q: %s", what, raw, why)
	}
	if hasControl(raw) || strings.ContainsAny(raw, " \t") {
		return bad("contains whitespace")
	}
	if strings.ContainsAny(raw, "?#") {
		return bad("must not carry a query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("not a URL")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return bad("must not carry a username or password")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return bad("has no host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !localHost(host) {
			return bad("http is only allowed for this machine or a private network; use https")
		}
	default:
		return bad("must be https")
	}
	return strings.TrimRight(raw, "/"), nil
}

// localHost is true for this machine, loopback and private-network hosts.
func localHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
	}
	return false
}

// ImportKeyHint shows that a key came with the link without revealing it.
func ImportKeyHint(key string) string {
	if key == "" {
		return "none (add one later with: magpie provider key <id> <key>)"
	}
	if len([]rune(key)) >= 12 {
		return string([]rune(key)[:4]) + "… (provided)"
	}
	return "provided"
}

// ImportHosts lists the hosts requests and the key will be sent to.
func ImportHosts(p Provider) []string {
	var out []string
	for _, pr := range Protocols {
		if h := HostOf(p.Base(pr)); h != "" && !contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// DescribeImport is the text shown to the user before an imported provider
// is saved. It never contains the full key.
func DescribeImport(p Provider) []string {
	lines := []string{
		"name:      " + p.Name,
		"id:        " + p.ID,
		"sends to:  " + strings.Join(ImportHosts(p), ", ") + "  (requests and the API key go there)",
	}
	for _, pr := range Protocols {
		if b := p.Base(pr); b != "" {
			lines = append(lines, fmt.Sprintf("%-10s %s", string(pr)+":", b))
		}
	}
	lines = append(lines, "key:       "+ImportKeyHint(p.Key))
	if len(p.Models) > 0 {
		shown := p.Models
		more := ""
		if len(shown) > 12 {
			more = fmt.Sprintf(" … %d more", len(shown)-12)
			shown = shown[:12]
		}
		lines = append(lines, "models:    "+strings.Join(shown, ", ")+more)
	} else {
		lines = append(lines, "models:    fetched from the vendor after adding")
	}
	return lines
}

// Exists reports whether a provider with the id is already configured.
func Exists(id string) bool {
	for _, p := range All() {
		if p.ID == id {
			return true
		}
	}
	return false
}
