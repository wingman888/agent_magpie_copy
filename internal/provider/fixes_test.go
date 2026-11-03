package provider

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeCannotServeAlongside(t *testing.T) {
	claudeHome(t)
	if err := SetLoginOn("claude", "a@example.com", true); err == nil {
		t.Fatal("a Claude sign-in was allowed to serve alongside the current one")
	}
}

func TestClaudeSwitchWithoutCurrentKeepsOtherFieldsAndDropsIdentity(t *testing.T) {
	home := claudeHome(t)
	// no usable sign-in now: the file holds other fields only
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"mcpOAuth": map[string]any{"plugin:slack": map[string]any{"serverName": "Slack"}},
	})
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups":  3,
		"oauthAccount": map[string]any{"emailAddress": "old@example.com"},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()

	// the target account has no oauthAccount of its own
	cred := map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "sk-new", "refreshToken": "r-new", "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
	}}
	if err := writeClaudeLogin(cred); err != nil {
		t.Fatal(err)
	}
	creds := readJSONFile(t, filepath.Join(home, ".claude", ".credentials.json"))
	oauth, _ := creds["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-new" {
		t.Fatalf("access token not written: %v", creds)
	}
	if creds["mcpOAuth"] == nil {
		t.Fatalf("other credential fields were lost: %v", creds)
	}
	state := readJSONFile(t, filepath.Join(home, ".claude.json"))
	if _, ok := state["oauthAccount"]; ok {
		t.Fatalf("the replaced account's identity was left behind: %v", state)
	}
	if state["numStartups"] != float64(3) {
		t.Fatalf("other state fields changed: %v", state)
	}
}

func TestSVGRulesRefuseLiveContent(t *testing.T) {
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"plain", `<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`, true},
		{"prolog", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`, true},
		{"script element", `<svg><script>alert(1)</script></svg>`, false},
		{"event handler", `<svg onload="alert(1)"></svg>`, false},
		{"event handler, namespaced", `<svg xmlns:x="u"><circle x:onclick="a()"/></svg>`, false},
		{"javascript link", `<svg><a href="javascript:alert(1)"><circle r="1"/></a></svg>`, false},
		{"javascript link with spaces", `<svg><a href=" JavaScript:alert(1)"/></svg>`, false},
		{"foreignObject", `<svg><foreignObject><div>hi</div></foreignObject></svg>`, false},
		{"iframe", `<svg><iframe src="x"/></svg>`, false},
		{"embed", `<svg><embed src="x"/></svg>`, false},
		{"object", `<svg><object data="x"/></svg>`, false},
		{"doctype", `<!DOCTYPE svg [<!ENTITY a "b">]><svg/>`, false},
		{"not svg root", `<html><body>hi</body></html>`, false},
		{"broken xml", `<svg><circle></svg>`, false},
		{"plain text", `hello world`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSVG([]byte(c.body)); got != c.ok {
				t.Errorf("isSVG = %v, want %v", got, c.ok)
			}
		})
	}
}

func TestSaveRefusesUnknownBuiltinIconName(t *testing.T) {
	isolateConfig(t)
	base := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk"}
	bad := base
	bad.Icon = "no-such-logo"
	if err := Save(bad); err == nil {
		t.Fatal("an icon name that is neither a stored picture nor built in was saved")
	}
	good := base
	good.Icon = "openai"
	if err := Save(good); err != nil {
		t.Fatalf("built-in icon refused: %v", err)
	}
	empty := base
	empty.Icon = ""
	if err := Save(empty); err != nil {
		t.Fatalf("empty icon refused: %v", err)
	}
}
