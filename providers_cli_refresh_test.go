package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// `dial provider models copilot` must show the list it just fetched, not the
// copy of the provider taken before the refresh.
func TestProviderModelsRefreshShowsFreshList(t *testing.T) {
	cliIsolate(t)
	// A token no other test exchanges, so no cached session is reused.
	cliWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com:refresher":{"user":"refresher","oauth_token":"gho_refresh_cli"}}`)

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_refresh_cli" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-cli",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": srv.URL + "/api"},
		})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-cli" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"only-new-model","name":"Only New","capabilities":{"type":"chat"},"model_picker_enabled":true}]}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { provider.CopilotTokenURL = old })

	out, err := cliCapture(t, func() error { return providerCmd([]string{"provider", "models", "copilot"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 models from") {
		t.Errorf("refresh did not report the fetch:\n%s", out)
	}
	if !strings.Contains(out, "copilot/only-new-model") {
		t.Errorf("detail after refresh still shows the pre-refresh list:\n%s", out)
	}
}
