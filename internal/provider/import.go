package provider

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links: magpie://import?….
const Scheme = "magpie"

const (
	importNameMax   = 80   // runes
	importKeyMax    = 4096 // bytes
	importModelsMax = 200
)

// catalogIDRe matches a models.dev provider id.
var catalogIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport turns an import link into the provider it describes. It saves
// nothing, writes nothing and makes no network calls: the caller shows the
// result to the user and saves it only after they agree.
//
// Link form: magpie://import?<params>, with every value URL-encoded. The
// equivalent spellings magpie:import?…, magpie:///import?… and
// MAGPIE://import/?… are accepted too.
func ParseImport(link string) (Provider, error) {
	link = strings.TrimSpace(link)
	if scheme, _, ok := strings.Cut(link, ":"); !ok || !strings.EqualFold(scheme, Scheme) {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	u, err := url.Parse(link)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %v", err)
	}
	action := u.Host + u.Path
	if u.Opaque != "" {
		action = u.Opaque
	}
	if action = strings.Trim(action, "/"); !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("unsupported link action %q: the only action is import", action)
	}
	// ParseQuery is strict: a malformed escape is an error, not a dropped field.
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import parameters: %v", err)
	}
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var pr *PresetDef
	if presetID := strings.ToLower(get("preset")); presetID != "" {
		if pr = Preset(presetID); pr == nil {
			return Provider{}, fmt.Errorf("no preset %q — magpie presets lists them", presetID)
		}
	}

	var p Provider
	if pr != nil {
		// Endpoints, catalog and links come from the preset; the link's own
		// base URLs, catalog, website and keys page do not apply.
		p, _ = FromPreset(pr.ID)
		if r := get("region"); r != "" {
			reg, err := presetRegion(pr, r)
			if err != nil {
				return Provider{}, err
			}
			if reg.Chat != "" {
				p.Chat = reg.Chat
			}
			if reg.Responses != "" {
				p.Responses = reg.Responses
			}
			if reg.Anthropic != "" {
				p.Anthropic = reg.Anthropic
			}
		}
	}

	// Check the raw name first: trimming would hide a trailing %0A.
	if hasControl(q.Get("name")) {
		return Provider{}, fmt.Errorf("provider name contains control characters")
	}
	name := get("name")
	if pr == nil && name == "" {
		return Provider{}, fmt.Errorf("import link needs a preset or a name")
	}
	if name != "" {
		p.Name = truncateRunes(name, importNameMax)
	}

	switch idRaw := get("id"); {
	case idRaw != "":
		p.ID = Slug(idRaw)
	case pr != nil:
		// FromPreset already set the preset's own id
	default:
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, fmt.Errorf("cannot derive a provider id from %q: pass id=<id>", name)
	}
	if p.ID == Scheme {
		return Provider{}, fmt.Errorf("%q is reserved; pick another id", Scheme)
	}

	// The key is taken raw: trimming would hide a trailing %0A.
	key := q.Get("key")
	if len(key) > importKeyMax {
		return Provider{}, fmt.Errorf("API key longer than %d bytes", importKeyMax)
	}
	if hasControl(key) {
		return Provider{}, fmt.Errorf("API key contains control characters")
	}
	p.Key = key

	p.Models = cleanList(strings.Split(q.Get("models"), ","))
	if len(p.Models) > importModelsMax {
		p.Models = p.Models[:importModelsMax]
	}

	if pr == nil {
		if c := strings.ToLower(get("catalog")); catalogIDRe.MatchString(c) {
			p.Catalog = c
			p.Icon = IconForCatalog(c)
		}
		p.Website = httpsOnly(get("website"))
		p.KeysURL = httpsOnly(get("keys"))

		if p.Chat, err = importBase("chat", q.Get("chat")); err != nil {
			return Provider{}, err
		}
		if p.Responses, err = importBase("responses", q.Get("responses")); err != nil {
			return Provider{}, err
		}
		if p.Anthropic, err = importBase("anthropic", q.Get("anthropic")); err != nil {
			return Provider{}, err
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			return Provider{}, fmt.Errorf("a custom import needs at least one of chat, responses or anthropic")
		}
	}
	return p, nil
}

// presetRegion finds one of a preset's regions by id.
func presetRegion(pr *PresetDef, id string) (Region, error) {
	var ids []string
	for _, r := range pr.Regions {
		if strings.EqualFold(r.ID, id) {
			return r, nil
		}
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return Region{}, fmt.Errorf("preset %q has no regions", pr.ID)
	}
	return Region{}, fmt.Errorf("preset %q has no region %q (regions: %s)", pr.ID, id, strings.Join(ids, ", "))
}

// importBase validates one base URL from a link. An empty value is allowed
// and means "not given". The result has no trailing slash.
func importBase(param, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: malformed URL %q: %v", param, raw, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !localHost(u.Hostname()) {
			return "", fmt.Errorf("%s: %q must be https (plain http is only allowed for localhost, loopback, private-network IPs and .local hosts)", param, raw)
		}
	default:
		return "", fmt.Errorf("%s: %q must be an https URL", param, raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s: %q has no host", param, raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s: %q must not contain a username or password", param, raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("%s: %q must not contain a query string", param, raw)
	}
	if u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%s: %q must not contain a fragment", param, raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// httpsOnly keeps a link's website or keys-page URL only when it is https.
// Anything else is dropped without an error.
func httpsOnly(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// localHost reports whether a host may be reached over plain http:
// localhost, loopback, private-network IPs, and .local names.
func localHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// hasControl reports whether s contains any control character, newlines included.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
