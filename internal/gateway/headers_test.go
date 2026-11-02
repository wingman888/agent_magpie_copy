package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestForwardSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Token mine"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if v := got.Get("X-Org-Id"); v != "acme" {
		t.Errorf("X-Org-Id = %q", v)
	}
	if v := got.Values("Authorization"); len(v) != 1 || v[0] != "Token mine" {
		t.Errorf("Authorization = %q, want the user's", v)
	}
}

func TestForwardPresetSendsNoCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	// even if the field were set in memory, a preset never sends it
	p := provider.Provider{ID: "ds", Name: "DS", Preset: "deepseek", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme"}}
	if len(p.ExtraHeaders()) != 0 {
		t.Fatal("preset has extra headers")
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if code, body := post(t, "/v1/chat/completions", `{"model":"ds/m1","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if v := got.Get("X-Org-Id"); v != "" {
		t.Errorf("preset sent X-Org-Id = %q", v)
	}
}
