package provider

// Import links: magpie://import?preset=…&key=… (or a custom vendor with
// name= and a base URL). A link only describes a provider; ParseImport
// checks it and returns the result, and nothing is saved until the user
// has seen what it would add.

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
	importMaxModels = 200
	importMaxKey    = 4096
	importMaxName   = 80
)

// importFields are the parameters a link may carry. Unknown ones are ignored.
var importFields = []string{"preset", "region", "name", "id", "key", "chat", "responses", "anthropic",
	"models", "catalog", "website", "keys"}

// ParseImport parses a magpie://import link into the provider it describes.
// Any invalid part rejects the whole link; nothing is saved.
func ParseImport(link string) (Provider, error) {
	query, err := importQuery(link)
	if err != nil {
		return Provider{}, err
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("import link: %v", err)
	}
	f := map[string]string{}
	for _, k := range importFields {
		if len(vals[k]) > 1 {
			return Provider{}, fmt.Errorf("import link: %q is given more than once", k)
		}
		if len(vals[k]) == 1 {
			f[k] = vals[k][0]
		}
	}

	var p Provider
	var pr *PresetDef
	if want := strings.ToLower(strings.TrimSpace(f["preset"])); want != "" {
		pr = Preset(want)
		if pr == nil {
			return Provider{}, fmt.Errorf("import link: no preset %q — magpie presets lists them", want)
		}
		p = Provider{ID: pr.ID, Name: pr.Name, Icon: pr.Icon, Preset: pr.ID,
			Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic,
			Catalog: pr.Catalog, Website: pr.Website, KeysURL: pr.KeysURL}
		if want := strings.TrimSpace(f["region"]); want != "" {
			r, err := importRegion(pr, want)
			if err != nil {
				return Provider{}, err
			}
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
		}
	} else if strings.TrimSpace(f["region"]) != "" {
		return Provider{}, errors.New("import link: region needs a preset")
	}

	name, err := importName(f["name"])
	if err != nil {
		return Provider{}, err
	}
	if name != "" {
		p.Name = name
	}
	if pr == nil && p.Name == "" {
		return Provider{}, errors.New("import link: name is required without a preset")
	}

	switch id := strings.TrimSpace(f["id"]); {
	case id != "":
		if id != Slug(id) {
			return Provider{}, fmt.Errorf("import link: id %q must be lowercase letters, digits and dashes; try %q", id, Slug(id))
		}
		p.ID = id
	case pr != nil:
		p.ID = pr.ID
	default:
		p.ID = Slug(p.Name)
		if p.ID == "" {
			return Provider{}, fmt.Errorf("import link: name %q has no letters or digits to make an id from; give id=", p.Name)
		}
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`import link: "magpie" is what agents call the gateway itself; pick another id`)
	}

	if err := importKey(f["key"], &p); err != nil {
		return Provider{}, err
	}

	for _, b := range []struct {
		field string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		v := strings.TrimSpace(f[b.field])
		if v == "" {
			continue
		}
		u, err := importBase(b.field, v)
		if err != nil {
			return Provider{}, err
		}
		*b.dst = u
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("import link: needs a base URL (chat, responses or anthropic)")
	}

	if models := cleanList(strings.Split(f["models"], ",")); len(models) > importMaxModels {
		return Provider{}, fmt.Errorf("import link: at most %d models, got %d", importMaxModels, len(models))
	} else if len(models) > 0 {
		p.Models = models
	}
	if c := strings.TrimSpace(f["catalog"]); c != "" {
		p.Catalog = c
	}
	if u, ok := importHTTPS(f["website"]); ok {
		p.Website = u
	}
	if u, ok := importHTTPS(f["keys"]); ok {
		p.KeysURL = u
	}
	return p, nil
}

// importQuery accepts magpie://import?…, magpie:import?…, magpie:///import?…
// and import/ in any letter case of the scheme, and returns the query string.
func importQuery(link string) (string, error) {
	s := strings.TrimSpace(link)
	const scheme = "magpie:"
	if len(s) < len(scheme) || !strings.EqualFold(s[:len(scheme)], scheme) {
		return "", errors.New("not a magpie link; expected magpie://import?…")
	}
	s = s[len(scheme):]
	if strings.HasPrefix(s, "//") {
		s = s[2:]
	}
	s = strings.TrimPrefix(s, "/")
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	path, query, _ := strings.Cut(s, "?")
	if action := strings.TrimSuffix(path, "/"); !strings.EqualFold(action, "import") {
		return "", fmt.Errorf("magpie link action %q is not supported; only import", action)
	}
	return query, nil
}

// importRegion picks one of a preset's regional endpoint sets by id or name.
func importRegion(pr *PresetDef, want string) (Region, error) {
	for _, r := range pr.Regions {
		if strings.EqualFold(r.ID, want) || strings.EqualFold(r.Name, want) {
			return r, nil
		}
	}
	return Region{}, fmt.Errorf("import link: %s has no region %q", pr.Name, want)
}

// importName trims the name and cuts it to 80 characters.
func importName(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("import link: name has control characters")
		}
	}
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > importMaxName {
		name = strings.TrimSpace(string([]rune(name)[:importMaxName]))
	}
	return name, nil
}

// importKey checks the key as written (a newline in it is an error, not
// something to trim away) and sets it on the provider.
func importKey(raw string, p *Provider) error {
	if len(raw) > importMaxKey {
		return fmt.Errorf("import link: key is longer than %d characters", importMaxKey)
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return errors.New("import link: key has control characters")
		}
	}
	p.Key = strings.TrimSpace(raw)
	return nil
}

// importBase checks one protocol's base URL: https, or http only for hosts on
// this machine, a private network or a .local name; no user name, query or
// fragment. A trailing slash is dropped.
func importBase(field, raw string) (string, error) {
	s := strings.TrimRight(raw, "/")
	if strings.ContainsAny(s, "?#") {
		return "", fmt.Errorf("import link: %s must not carry a query or fragment", field)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("import link: %s %q is not a URL", field, raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("import link: %s must not carry a user name or password", field)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return "", fmt.Errorf("import link: %s must be https (http is only allowed for this machine, a private network or .local)", field)
		}
	default:
		return "", fmt.Errorf("import link: %s scheme %q is not allowed; use https", field, u.Scheme)
	}
	return s, nil
}

// isLocalHost is true for names and addresses that stay on this machine or
// its private network.
func isLocalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())
}

// importHTTPS returns a website or key-page URL when it is https; anything
// else is ignored, so the link still imports.
func importHTTPS(raw string) (string, bool) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	return s, true
}
