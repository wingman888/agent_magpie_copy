package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// An ordinary provider is probed at its own base with its key, as before.
func TestFxPlainTestProbeUsesBaseAndKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	p := Provider{ID: "fxplain", Name: "FxPlain", Key: "k", Chat: srv.URL + "/v1", Models: []string{"m1"}}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("results %+v", res)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer k" {
		t.Errorf("path %q auth %q", gotPath, gotAuth)
	}
}

// A GitHub entry with no OAuth token is not a Copilot sign-in.
func TestFxCopilotEntryWithoutTokenIsNotSignedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	dir := filepath.Join(home, "xdg", "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"),
		[]byte(`{"github.com":{"user":"fx-octo"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCopilot(); ok {
		t.Fatal("an entry without oauth_token counted as signed in")
	}
	if err := os.WriteFile(filepath.Join(dir, "apps.json"),
		[]byte(`{"github.com:Iv1.fx":{"user":"fx-octo","oauth_token":"gho_fx"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, ok := readCopilot(); !ok || e.OAuthToken != "gho_fx" {
		t.Fatalf("token entry not found: %+v %v", e, ok)
	}
}
