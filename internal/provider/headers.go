package provider

import (
	"net/http"

	"github.com/yetone/magpie/internal/catalog"
)

// customHeaders are the extra headers magpie sends to a provider. Only custom
// providers (a plain URL and key) have them: presets keep their own headers,
// and a signed-in agent is never sent the user's.
func (p Provider) customHeaders() map[string]string {
	if p.Account != nil || Preset(p.Preset) != nil {
		return nil
	}
	return p.Headers
}

// ApplyHeaders adds the custom headers to req, under the names as typed. Call
// it after Sign, so a custom header of the same name as an auth header wins.
func (p Provider) ApplyHeaders(req *http.Request) {
	catalog.SetHeaders(req.Header, p.customHeaders())
}
