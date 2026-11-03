package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Forwarding sends a custom provider's headers after the auth header, so a
// header the user sets can override a default; presets send none.
func TestForwardAppliesCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer up.Close()

	s := New()
	custom := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer override"}}
	res, err := s.forward(context.Background(), custom, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got.Get("X-Org-Id") != "acme" {
		t.Errorf("custom header not forwarded: %v", got)
	}
	if got.Get("Authorization") != "Bearer override" {
		t.Errorf("custom header should override the auth default: %v", got.Get("Authorization"))
	}

	preset := provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	res, err = s.forward(context.Background(), preset, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got.Get("X-Org-Id") != "" {
		t.Errorf("preset should not send custom headers: %v", got)
	}
}
