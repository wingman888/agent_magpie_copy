// Package extrahdr applies a user's own request headers (a provider's
// "headers" map) to an outgoing request.
package extrahdr

import (
	"net/http"
	"sort"
	"strings"
)

// Set writes extra onto h after the defaults and the auth headers, so the
// user's values win. Names keep the case the user typed: http.Header would
// canonicalise them, so the value is stored under the name as given. Any
// existing entry with the same name, in any case, is replaced first.
func Set(h http.Header, extra map[string]string) {
	names := make([]string, 0, len(extra))
	for k := range extra {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		for k := range h {
			if strings.EqualFold(k, name) {
				delete(h, k)
			}
		}
		h[name] = []string{extra[name]}
	}
}
