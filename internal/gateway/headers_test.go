package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom provider's headers ride along on a request the gateway forwards,
// after the key's own Authorization.
func TestGatewayForwardCarriesCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if got == nil {
		t.Fatal("upstream never got the request")
	}
	if v := got.Get("X-Org-Id"); v != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", v)
	}
	if v := got.Get("Authorization"); v != "Bearer k" {
		t.Errorf("Authorization = %q, want the key", v)
	}
}
