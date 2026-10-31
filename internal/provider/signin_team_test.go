package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Team seat on the email of a personal subscription is an account of its
// own: added beside the personal one, which stays signed in; and signing in
// brings back a Claude account that had been removed from magpie.
func TestClaudeTeamSeatBesidePersonal(t *testing.T) {
	home := claudeHome(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "sk-ant-oat01-team", "refresh_token": "sk-ant-ort01-team", "expires_in": 3600,
				"account":      map[string]any{"uuid": "u", "email_address": "same@example.com"},
				"organization": map[string]any{"uuid": "o-team", "name": "Acme"},
			})
		case "/api/oauth/profile":
			json.NewEncoder(w).Encode(map[string]any{
				"account":      map[string]any{"email": "same@example.com"},
				"organization": map[string]any{"organization_type": "claude_team"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	claudeTokenURL, claudeBase = fake.URL+"/v1/oauth/token", fake.URL

	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{
		"emailAddress": "same@example.com", "organizationUuid": "o-personal", "organizationName": "same@example.com's Organization"}})
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("removed account still listed")
	}

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "same@example.com · Acme" || st.Plan != "team" || st.Using {
		t.Fatalf("state %+v", st)
	}
	users, active := loginUsers(Logins("claude"))
	if strings.Join(users, ",") != "same@example.com,same@example.com · Acme" || active != "same@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	var c map[string]map[string]any
	readJSON(cred, &c)
	if c["claudeAiOauth"]["refreshToken"] == "sk-ant-ort01-team" {
		t.Fatal("the personal sign-in was replaced")
	}
	p, ok := find(All(), "claude")
	if !ok || len(p.AlsoOn()) != 1 || p.AlsoOn()[0].Account.User != "same@example.com · Acme" {
		t.Fatalf("account after sign-in: %v %+v", ok, p.AlsoOn())
	}
}

func TestSaveKeepsAccountIDs(t *testing.T) {
	claudeHome(t)
	if err := Save(Provider{Name: "Claude", Anthropic: "https://relay.example.com", Key: "k"}); err == nil {
		t.Fatal("a relay took the Claude subscription's id")
	}
	if id := freeID("claude"); id == "claude" {
		t.Fatal("import took the Claude subscription's id")
	}
}
