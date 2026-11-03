package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Every request the gateway forwards to a custom provider carries its
// headers, and they win over the auth headers of the same name.
func TestForwardSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, reply: sse(`data: [DONE]`)}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)

	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Token mine"}}
	res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	if got := f.head.Get("X-Org-Id"); got != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", got)
	}
	if got := f.head.Get("Authorization"); got != "Token mine" {
		t.Errorf("Authorization = %q, want the user's value", got)
	}
}

// A preset vendor forwards with its own auth only.
func TestForwardPresetSendsNoCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, reply: sse(`data: [DONE]`)}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)

	p := provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	if got := f.head.Get("X-Org-Id"); got != "" {
		t.Errorf("preset sent X-Org-Id = %q", got)
	}
	if got := f.head.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q", got)
	}
}
