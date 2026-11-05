package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJWTPayloadAcceptsAnyBase64Flavour(t *testing.T) {
	claims := `{"email":"a+b/c@example.com","exp":1900000000}`
	std := base64.StdEncoding.EncodeToString([]byte(claims))   // padded, standard
	raw := base64.RawStdEncoding.EncodeToString([]byte(claims)) // unpadded, standard
	url := base64.URLEncoding.EncodeToString([]byte(claims))    // padded, url
	rawURL := base64.RawURLEncoding.EncodeToString([]byte(claims))
	for _, seg := range []string{std, raw, url, rawURL} {
		m := jwtPayload("h." + seg + ".s")
		if m == nil || m["email"] != "a+b/c@example.com" {
			t.Fatalf("segment %q decoded to %v", seg, m)
		}
	}
	if jwtPayload("not-a-jwt") != nil || jwtPayload("h.!!!.s") != nil {
		t.Fatal("garbage must decode to nil")
	}
}

func TestCodexIdentityFallbacks(t *testing.T) {
	home := setupAgents(t)
	exp := time.Now().Add(time.Hour).Unix()
	acc := jwt(map[string]any{"exp": exp})

	// id_token undecodable, no email anywhere: still signed in, named "codex"
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"bad","access_token":"`+acc+`","refresh_token":"rt"}}`)
	a, ok := codexAccount()
	if !ok || a.User != "codex" {
		t.Fatalf("undecodable id_token: ok=%v account=%+v", ok, a)
	}

	// no email but a name claim: the name is shown
	idTok := jwt(map[string]any{"name": "Ada", "sub": "u-1"})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"`+idTok+`","access_token":"`+acc+`"}}`)
	if a, ok := codexAccount(); !ok || a.User != "Ada" {
		t.Fatalf("name fallback: ok=%v account=%+v", ok, a)
	}

	// access token without exp: signed in, no expiry, no refresh
	noExp := jwt(map[string]any{"sub": "x"})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"bad","access_token":"`+noExp+`"}}`)
	if a, ok := codexAccount(); !ok || !a.Expires.IsZero() {
		t.Fatalf("no exp: ok=%v account=%+v", ok, a)
	}
	if codexExpiresSoon(noExp, time.Now()) {
		t.Fatal("a token without exp must not trigger refresh")
	}

	// tokens missing, or chatgpt mode without an access token: signed out
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt"}`)
	if _, ok := codexAccount(); ok {
		t.Fatal("missing tokens must be signed out")
	}
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"x","access_token":""}}`)
	if _, ok := codexAccount(); ok {
		t.Fatal("empty access token must be signed out")
	}
}

func TestCopilotLoginNeedsUserAndTokenAndIgnoresCase(t *testing.T) {
	setupAgents(t)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")

	// first entry lacks a token; the next complete one wins, key case aside
	writeFile(t, path, `{"GitHub.com:one":{"user":"ghost","oauth_token":""},"github.com:two":{"user":"octo","oauth_token":"gho_x"}}`)
	if u, o, ok := copilotLogin(); !ok || u != "octo" || o != "gho_x" {
		t.Fatalf("got %q %q %v", u, o, ok)
	}

	// an entry without a user is not a login
	writeFile(t, path, `{"github.com":{"user":"","oauth_token":"gho_x"}}`)
	if _, _, ok := copilotLogin(); ok {
		t.Fatal("empty user must be signed out")
	}
}

func TestCopilotFilterExtraRules(t *testing.T) {
	var rows []copilotRow
	body := `[
		{"id":"gpt-5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"nomic-embedding","capabilities":{"type":"chat"}},
		{"id":"router","name":"Internal Router","capabilities":{"type":"chat"}},
		{"id":"no-state","capabilities":{"type":"chat"},"policy":{}},
		{"id":"off","capabilities":{"type":"chat"},"policy":{"state":"unconfigured"}}
	]`
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	got := copilotChatModels(rows)
	ids := ""
	for _, m := range got {
		ids += m.ID + " "
	}
	if ids != "gpt-5 no-state " {
		t.Fatalf("kept %q", ids)
	}
}
