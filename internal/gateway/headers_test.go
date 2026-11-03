package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom provider's headers go upstream with every forwarded request,
// after the key, and a preset's do not.
func TestForwardSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, reply: `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, ctype: "application/json"}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"m1"},
		Chat:    up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer gateway"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.head.Get("X-Org-Id"); got != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", got)
	}
	// a custom header overrides the auth header magpie set
	if got := f.head.Get("Authorization"); got != "Bearer gateway" {
		t.Errorf("Authorization = %q, want the custom override", got)
	}
}

func TestForwardOmitsHeadersForPreset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, reply: `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, ctype: "application/json"}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	// a preset-backed provider whose file carries headers: they are ignored
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Preset: "deepseek", Key: "k", Models: []string{"m1"},
		Chat:    up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := post(t, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.head.Get("X-Org-Id"); got != "" {
		t.Errorf("X-Org-Id = %q, want none for a preset", got)
	}
}
