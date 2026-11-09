package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestHdrForwardCarriesCustomHeaders checks that a forwarded request to a
// custom vendor carries its headers, and that a preset's upstream does not.
func TestHdrForwardCarriesCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	got := make(chan http.Header, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	s := New()
	custom := provider.Provider{ID: "hdr-fwd", Name: "Relay", Chat: up.URL + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer override"}}
	res, err := s.forward(context.Background(), custom, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	h := <-got
	if v := h.Get("X-Org-Id"); v != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", v)
	}
	if v := h.Get("Authorization"); v != "Bearer override" {
		t.Errorf("Authorization = %q, want the header override to win", v)
	}

	preset := provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: up.URL + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	res, err = s.forward(context.Background(), preset, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if v := (<-got).Get("X-Org-Id"); v != "" {
		t.Errorf("preset sent X-Org-Id=%q, want none", v)
	}
}
