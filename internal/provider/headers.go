package provider

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// cleanHeaders trims names and values, drops entries with no name, and
// returns nil for an empty map so the field is left out of providers.json.
func cleanHeaders(h map[string]string) map[string]string {
	var out map[string]string
	for k, v := range h {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// validateHeaders rejects names that cannot be sent on the wire, and values
// with line breaks or other control characters.
func validateHeaders(h map[string]string) error {
	for k, v := range h {
		if !validHeaderName(k) {
			return fmt.Errorf("header name %q is not valid", k)
		}
		for _, r := range v {
			if r < ' ' && r != '\t' || r == 0x7f {
				return fmt.Errorf("header %s has a control character in its value", k)
			}
		}
	}
	return nil
}

// validHeaderName is the RFC 7230 token rule.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// ExtraHeaders are the headers the user attached to a custom vendor. Presets
// and signed-in accounts never get any.
func (p Provider) ExtraHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// applyHeaders sets the extra headers on a request, after the auth headers,
// so they can override defaults. Names keep the casing the user typed: Go
// canonicalises through Set, so the canonical entry is removed and the raw
// name written straight into the map.
func applyHeaders(req *http.Request, extra map[string]string) {
	names := make([]string, 0, len(extra))
	for k := range extra {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		req.Header.Del(k)
		req.Header[k] = []string{extra[k]}
	}
}
