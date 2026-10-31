package provider

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseImportAccepts(t *testing.T) {
	for _, l := range []string{
		"magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/",
		"magpie:import?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"magpie:///import?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"MAGPIE://IMPORT?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"magpie://import/?name=Acme%20Relay&chat=https://api.acme.example/v1",
	} {
		p, err := ParseImport(l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if p.ID != "acme-relay" || p.Name != "Acme Relay" || p.Chat != "https://api.acme.example/v1" {
			t.Fatalf("%s: %+v", l, p)
		}
	}
}

func TestParseImportFields(t *testing.T) {
	q := url.Values{}
	q.Set("name", "My Relay")
	q.Set("id", "mine")
	q.Set("key", "sk-123")
	q.Set("anthropic", "http://192.168.1.5:8080/")
	q.Set("responses", "http://localhost:1/v1")
	q.Set("models", " a, ,b,a ,c")
	q.Set("catalog", "deepseek")
	q.Set("website", "https://x.example")
	q.Set("keys", "http://insecure.example")
	p, err := ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "mine" || p.Key != "sk-123" || p.Anthropic != "http://192.168.1.5:8080" || p.Responses != "http://localhost:1/v1" ||
		strings.Join(p.Models, ",") != "a,b,c" || p.Catalog != "deepseek" || p.Website != "https://x.example" || p.KeysURL != "" {
		t.Fatalf("%+v", p)
	}
}

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-x")
	if err != nil || p.ID != "deepseek" || p.Preset != "deepseek" || p.Chat != "https://api.deepseek.com/v1" || p.Key != "sk-x" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = ParseImport("magpie://import?preset=deepseek&name=Mine")
	if err != nil || p.ID != "deepseek" || p.Name != "Mine" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
	var withRegions *PresetDef
	for _, pr := range Presets() {
		if len(pr.Regions) > 1 {
			pr := pr
			withRegions = &pr
			break
		}
	}
	if withRegions != nil {
		r := withRegions.Regions[1]
		p, err := ParseImport("magpie://import?preset=" + withRegions.ID + "&region=" + r.ID)
		if err != nil || p.Chat != r.Chat || p.Anthropic != r.Anthropic {
			t.Fatalf("%+v %v", p, err)
		}
	}
}

func TestParseImportRejects(t *testing.T) {
	long := strings.Repeat("k", 4097)
	for _, l := range []string{
		"https://import?name=a&chat=https://a.example",
		"magpie://delete?name=a&chat=https://a.example",
		"magpie://importer?name=a&chat=https://a.example",
		"magpie://import",
		"magpie://import?chat=https://a.example",
		"magpie://import?name=a",
		"magpie://import?name=a&chat=http://api.example.com/v1",
		"magpie://import?name=a&chat=https://u:p@api.example.com/v1",
		"magpie://import?name=a&chat=file:///etc/passwd",
		"magpie://import?name=a&chat=https://api.example.com/v1%3Fx%3D1",
		"magpie://import?name=a&chat=https://api.example.com/v1%23x",
		"magpie://import?name=a&id=magpie&chat=https://a.example",
		"magpie://import?name=a&id=Bad%20Id&chat=https://a.example",
		"magpie://import?name=%21%21%21&chat=https://a.example",
		"magpie://import?name=magpie&chat=https://a.example",
		"magpie://import?name=a&chat=https://a.example&key=sk%0Aabc",
		"magpie://import?name=a&chat=https://a.example&key=" + long,
		"magpie://import?preset=nonesuch",
	} {
		if p, err := ParseImport(l); err == nil {
			t.Errorf("accepted %s: %+v", l, p)
		}
	}
}

func TestParseImportLimits(t *testing.T) {
	name := strings.Repeat("n", 100)
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	p, err := ParseImport("magpie://import?id=x&chat=https://a.example&name=" + name + "&models=" + strings.Join(ms, ","))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Name) != 80 || len(p.Models) != 200 {
		t.Fatalf("%d %d", len(p.Name), len(p.Models))
	}
}

func TestSaveKeylessAndPrivateHosts(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p, err := ParseImport("magpie://import?name=Acme&chat=https://api.acme.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(p); err == nil {
		t.Fatal("Save without key succeeded")
	}
	if err := SaveKeyless(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("acme")
	if err != nil || got.Ready() {
		t.Fatalf("%+v %v", got, err)
	}
	for _, h := range []string{"127.0.0.1", "10.0.0.2", "[::1]", "192.168.0.1", "mac.local", "localhost:8080"} {
		if _, err := ParseImport("magpie://import?name=a&chat=http://" + h + "/v1"); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	if _, err := ParseImport("magpie://import?name=a&chat=http://8.8.8.8/v1"); err == nil {
		t.Error("public http accepted")
	}
}

func TestHideAndRestoreAccount(t *testing.T) {
	signIn(t)
	claudeAuthStatus = func() ([]byte, bool) { return nil, false }
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("codex still listed")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves")
	}
	ex := Excluded()
	if len(ex) != 1 || ex[0].Provider != "codex" || ex[0].Agent != "codex" || !strings.Contains(ex[0].Why, "provider add codex") {
		t.Fatalf("%+v", ex)
	}
	// the agent's own login is untouched
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("copilot"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "codex"}); err != nil {
		t.Fatal(err)
	}
	c, ok := find(All(), "codex")
	if !ok || len(c.Models) != 1 || c.Models[0] != "gpt-5.5" || len(Excluded()) != 0 {
		t.Fatalf("%+v %v", c, Excluded())
	}
}

func TestClaudeSignedOutIsNotListed(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	old := claudeAuthStatus
	t.Cleanup(func() { claudeAuthStatus = old; forgetClaudeCredential() })
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "a", "refreshToken": "r", "expiresAt": time.Now().Add(time.Hour).UnixMilli()},
	})
	cases := []struct {
		out    string
		ran    bool
		listed bool
	}{
		{`{"loggedIn": true, "email": "a@b.c"}`, true, true},
		{`{"loggedIn": false}`, true, false}, // even if the command exited non-zero
		{`not json`, true, true},
		{``, false, true},
	}
	for _, c := range cases {
		forgetClaudeCredential()
		claudeAuthStatus = func() ([]byte, bool) { return []byte(c.out), c.ran }
		_, ok := find(All(), "claude")
		if ok != c.listed {
			t.Errorf("%q ran=%v: listed=%v", c.out, c.ran, ok)
		}
	}
}
