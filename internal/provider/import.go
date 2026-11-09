package provider

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scheme is the URL scheme of import links: magpie://import?...
const Scheme = "magpie"

const (
	impMaxName   = 80   // runes kept from a link's name
	impMaxModels = 200  // models taken from a link's models list
	impMaxKey    = 4096 // bytes allowed in a link's key
)

// impParams are the query parameters an import link may carry; anything
// else is a typo or a feature this build does not know, so it is refused.
var impParams = map[string]bool{
	"preset": true, "region": true, "name": true, "id": true, "key": true,
	"chat": true, "responses": true, "anthropic": true, "models": true,
	"catalog": true, "website": true, "keys": true,
}

// impCatalogRe is the shape of a models.dev provider id.
var impCatalogRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseImport turns an import link into the provider it describes. It does
// not save anything and does not touch the network: the caller shows the
// result to the user and saves it only after they agree.
func ParseImport(link string) (Provider, error) {
	rest, ok := impStripScheme(strings.TrimSpace(link))
	if !ok {
		return Provider{}, fmt.Errorf("not a %s:// link", Scheme)
	}
	action, query, hasQuery := strings.Cut(rest, "?")
	if action = strings.TrimSuffix(action, "/"); !strings.EqualFold(action, "import") {
		return Provider{}, fmt.Errorf("unsupported %s link action %q; the only action is import", Scheme, action)
	}
	if !hasQuery || query == "" {
		return Provider{}, errors.New("import link has no parameters")
	}
	if strings.Contains(query, "#") {
		return Provider{}, errors.New("import link must not contain a fragment")
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return Provider{}, fmt.Errorf("malformed import link: %v", err)
	}
	if err := impCheckParams(vals); err != nil {
		return Provider{}, err
	}

	if vals.Get("preset") != "" {
		return impFromPreset(vals)
	}
	return impCustom(vals)
}

// impStripScheme accepts magpie://, magpie:, magpie:/// and any letter case,
// returning what follows the slashes.
func impStripScheme(s string) (string, bool) {
	prefix := Scheme + ":"
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimLeft(s[len(prefix):], "/"), true
}

// impCheckParams refuses unknown and repeated parameters.
func impCheckParams(vals url.Values) error {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !impParams[k] {
			return fmt.Errorf("unknown import parameter %q", k)
		}
		if len(vals[k]) > 1 {
			return fmt.Errorf("import parameter %q is given more than once", k)
		}
	}
	return nil
}

// impText reads a plain text parameter. Control characters are refused
// rather than stripped, so the user never gets silently different content.
func impText(vals url.Values, name string) (string, error) {
	raw := vals.Get(name)
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s contains control characters", name)
	}
	return strings.TrimSpace(raw), nil
}

// impModels splits the comma-separated models list.
func impModels(vals url.Values) ([]string, error) {
	raw, err := impText(vals, "models")
	if err != nil || raw == "" {
		return nil, err
	}
	models := cleanList(strings.Split(raw, ","))
	if len(models) > impMaxModels {
		models = models[:impMaxModels]
	}
	return models, nil
}

// impKey reads the API key. It is checked untrimmed, so a newline in the
// link is an error instead of being quietly dropped.
func impKey(vals url.Values) (string, error) {
	raw := vals.Get("key")
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", errors.New("key contains control characters")
	}
	key := strings.TrimSpace(raw)
	if len(key) > impMaxKey {
		return "", fmt.Errorf("key is longer than %d characters", impMaxKey)
	}
	return key, nil
}

// impFromPreset builds a provider from a preset, optionally on one of its
// regions. Endpoints, catalog, website and keys come from the preset; a
// name, id, key and models list in the link still apply.
func impFromPreset(vals url.Values) (Provider, error) {
	presetID, err := impText(vals, "preset")
	if err != nil {
		return Provider{}, err
	}
	// preset ids are lowercase; match case-insensitively, as provider add does
	presetID = strings.ToLower(presetID)
	p, err := FromPreset(presetID)
	if err != nil {
		return Provider{}, err
	}
	regionID, err := impText(vals, "region")
	if err != nil {
		return Provider{}, err
	}
	if regionID != "" {
		region, ok := impRegion(presetID, regionID)
		if !ok {
			return Provider{}, fmt.Errorf("preset %q has no region %q", presetID, regionID)
		}
		p.Chat, p.Responses, p.Anthropic = region.Chat, region.Responses, region.Anthropic
	}
	for _, u := range []*string{&p.Chat, &p.Responses, &p.Anthropic} {
		if *u != "" {
			if *u, err = impBaseURL(*u); err != nil {
				return Provider{}, err
			}
		}
	}
	name, err := impName(vals)
	if err != nil {
		return Provider{}, err
	}
	if name != "" {
		p.Name = name
	}
	if id, err := impText(vals, "id"); err != nil {
		return Provider{}, err
	} else if id != "" {
		p.ID = Slug(id)
	}
	if err := impApplyCommon(vals, &p); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// impRegion finds a region of a preset by id.
func impRegion(presetID, regionID string) (Region, bool) {
	pr := Preset(presetID)
	if pr == nil {
		return Region{}, false
	}
	for _, r := range pr.Regions {
		if r.ID == regionID {
			return r, true
		}
	}
	return Region{}, false
}

// impName reads the name parameter, truncated to impMaxName runes.
func impName(vals url.Values) (string, error) {
	name, err := impText(vals, "name")
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(name) > impMaxName {
		name = strings.TrimSpace(string([]rune(name)[:impMaxName]))
	}
	return name, nil
}

// impCustom builds a fully described provider from name and base URLs.
func impCustom(vals url.Values) (Provider, error) {
	if vals.Get("region") != "" {
		return Provider{}, errors.New("region needs a preset")
	}
	name, err := impName(vals)
	if err != nil {
		return Provider{}, err
	}
	if name == "" {
		return Provider{}, errors.New("import link needs a preset or a name")
	}
	p := Provider{Name: name}

	for _, f := range []struct {
		param string
		dst   *string
	}{{"chat", &p.Chat}, {"responses", &p.Responses}, {"anthropic", &p.Anthropic}} {
		raw, err := impText(vals, f.param)
		if err != nil {
			return Provider{}, err
		}
		if raw == "" {
			continue
		}
		if *f.dst, err = impBaseURL(raw); err != nil {
			return Provider{}, fmt.Errorf("%s: %w", f.param, err)
		}
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, errors.New("import link needs at least one base URL: chat, responses or anthropic")
	}

	id, err := impText(vals, "id")
	if err != nil {
		return Provider{}, err
	}
	if id == "" {
		id = name
	}
	p.ID = Slug(id)
	if p.ID == "" {
		return Provider{}, fmt.Errorf("cannot derive an id from %q; pass id=", id)
	}

	raw, err := impText(vals, "catalog")
	if err != nil {
		return Provider{}, err
	}
	if impCatalogRe.MatchString(raw) {
		p.Catalog = raw
	}
	p.Website = impHTTPSLink(vals, "website")
	p.KeysURL = impHTTPSLink(vals, "keys")

	if err := impApplyCommon(vals, &p); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// impApplyCommon fills in the key and the model picks, then checks the id.
// Presets and custom links share it.
func impApplyCommon(vals url.Values, p *Provider) error {
	key, err := impKey(vals)
	if err != nil {
		return err
	}
	p.Key = key

	models, err := impModels(vals)
	if err != nil {
		return err
	}
	p.Models = models

	if p.ID == "" {
		return errors.New("provider id is empty")
	}
	if p.ID == "magpie" {
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if impAccountID(p.ID) {
		return fmt.Errorf("id %q is reserved by a logged-in agent account; pass id=<other> to choose another id", p.ID)
	}
	return nil
}

// impAccountIDs are the ids of the agent accounts (see Accounts). A provider
// saved under one of them is discarded once that agent is signed in, so an
// import link may not claim these ids, even while signed out.
var impAccountIDs = []string{"claude", "codex", "copilot"}

func impAccountID(id string) bool {
	for _, a := range impAccountIDs {
		if id == a {
			return true
		}
	}
	return false
}

// impHTTPSLink returns the parameter when it is an https URL, else "".
func impHTTPSLink(vals url.Values, name string) string {
	raw, err := impText(vals, name)
	if err != nil || raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}

// impBaseURL validates an upstream base URL and drops its trailing slash.
// https is required; plain http only for hosts on this machine or the local
// network. Credentials, queries and fragments are refused.
func impBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %v", err)
	}
	if u.Opaque != "" || u.Host == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("%q must not contain a username or password", raw)
	}
	if u.ForceQuery || u.RawQuery != "" {
		return "", fmt.Errorf("%q must not contain a query string", raw)
	}
	if strings.Contains(raw, "#") {
		return "", fmt.Errorf("%q must not contain a fragment", raw)
	}
	switch u.Scheme {
	case "https":
		return raw, nil
	case "http":
		if impLocalHost(u.Hostname()) {
			return raw, nil
		}
		return "", fmt.Errorf("%q must use https (http is allowed only for localhost, loopback, private network or .local hosts)", raw)
	default:
		return "", fmt.Errorf("%q must use https", raw)
	}
}

// impLocalHost reports whether a host is on this machine or the local network.
func impLocalHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
