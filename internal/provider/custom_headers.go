package provider

import (
	"net/http"
	"strings"
)

// customHeaders are the extra headers the user set on a custom provider (a
// plain URL and key). Presets and signed-in agent accounts send none.
func (p Provider) customHeaders() map[string]string {
	if p.Account != nil || Preset(p.Preset) != nil {
		return nil
	}
	return p.Headers
}

// CustomHeaders is the set of extra headers every request to this provider
// must carry, keyed by name exactly as the user typed it. It is nil when
// there are none to send.
func (p Provider) CustomHeaders() map[string]string {
	return p.customHeaders()
}

// ApplyCustomHeaders sets the provider's custom headers on req. It must run
// after the auth headers are set: a custom header with the same name (in any
// letter case) replaces the auth header's value. Names are written straight
// into the header map so they go out exactly as the user typed them; Header.Set
// would canonicalise them.
func (p Provider) ApplyCustomHeaders(req *http.Request) {
	h := p.customHeaders()
	if len(h) == 0 {
		return
	}
	for name := range h {
		for k := range req.Header {
			if strings.EqualFold(k, name) {
				delete(req.Header, k)
			}
		}
	}
	for name, v := range h {
		req.Header[name] = []string{v}
	}
}
