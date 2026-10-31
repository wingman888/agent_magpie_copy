package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseGrokModels(t *testing.T) {
	out := "You are logged in with grok.com.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n  - grok-4.7-build-fast\n  - grok-4.6\n"
	ms := parseGrokModels(out)
	if len(ms) != 3 || ms[0].ID != "grok-4.7" || ms[1].ID != "grok-4.7-build-fast" || ms[2].ID != "grok-4.6" {
		t.Fatalf("models = %+v", ms)
	}
}

func TestGrokTokenReadsTheCLIsSignIn(t *testing.T) {
	home := t.TempDir()
	exp := time.Now().Add(2 * time.Hour).UTC()
	auth := map[string]any{"https://auth.x.ai::u1": map[string]any{
		"key": "tok", "email": "me@example.com", "auth_mode": "oidc", "refresh_token": "r",
		"expires_at": exp.Format(time.RFC3339Nano), "oidc_issuer": "https://auth.x.ai"}}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if u, ok := GrokUser(home); !ok || u != "me@example.com" {
		t.Fatalf("user = %q %v", u, ok)
	}
	var out bytes.Buffer
	if err := GrokToken(&out, home, "", false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
		Issuer  string `json:"issuer"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != "tok" || got.Issuer != "https://auth.x.ai" || got.Refresh != "" || got.Expires < 7000 || got.Expires > 7200 {
		t.Fatalf("token answer = %+v", got)
	}
	if err := GrokToken(&out, t.TempDir(), "", false); err == nil {
		t.Fatal("no sign-in, yet a token")
	}
}
