package provider

import (
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Custom headers: extra request headers a self-made vendor needs (an org id,
// a gateway's own token). They go on every request magpie makes to that
// vendor: forwarding, the connectivity test, and the model list. Presets and
// signed-in accounts never get them.

// CustomHeaders are the headers to send to the provider, or nil when it has
// none to send (presets and accounts never do).
func (p Provider) CustomHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// ApplyHeaders adds the provider's custom headers to req. Call it after the
// request is signed, so these can override the defaults. Names keep the case
// the user typed.
func ApplyHeaders(p Provider, req *http.Request) {
	h := p.CustomHeaders()
	for _, name := range sortedKeys(h) {
		catalog.SetHeaderExact(req.Header, name, h[name])
	}
}

// ValidHeader reports whether name is a legal HTTP header field name (an
// RFC 7230 token).
func ValidHeader(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// cleanHeaders trims names and values, drops entries with no name, an
// illegal name, or a value that would break the header line, and returns nil
// for an empty map so nothing is written.
func cleanHeaders(h map[string]string) map[string]string {
	var out map[string]string
	for _, k := range sortedKeys(h) {
		name, v := strings.TrimSpace(k), strings.TrimSpace(h[k])
		if !ValidHeader(name) || strings.ContainsAny(v, "\r\n") {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = v
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
