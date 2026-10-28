package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The upstream host does not resolve; only a proxy can answer for it.
func TestAcceptGatewayUsesSettingsProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	var hits atomic.Int32
	var host string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		host = r.Host
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg-via-proxy","type":"message","content":[]}`)
	}))
	defer px.Close()

	if err := settings.Save(settings.Settings{Proxy: px.URL}); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Anthropic: "http://upstream.invalid"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/m1","max_tokens":10,"messages":[]}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "msg-via-proxy") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if hits.Load() == 0 || !strings.HasPrefix(host, "upstream.invalid") {
		t.Fatalf("proxy hits=%d host=%q", hits.Load(), host)
	}
}
