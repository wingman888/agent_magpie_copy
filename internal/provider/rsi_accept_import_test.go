package provider_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptOpenCodePresets(t *testing.T) {
	want := map[string][5]string{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go", "https://opencode.ai/auth"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen", "https://opencode.ai/auth"},
	}
	for id, w := range want {
		pr := provider.Preset(id)
		if pr == nil {
			t.Errorf("no preset %s", id)
			continue
		}
		if pr.Chat != w[0] || pr.Anthropic != w[2] || pr.Website != w[3] || pr.KeysURL != w[4] {
			t.Errorf("%s: %+v", id, pr)
		}
		if pr.Responses != "" && pr.Responses != w[1] {
			t.Errorf("%s responses: %q", id, pr.Responses)
		}
		found := false
		for _, p := range provider.Presets() {
			found = found || p.ID == id
		}
		if !found {
			t.Errorf("%s missing from Presets()", id)
		}
		p, err := provider.ParseImport("magpie://import?preset=" + id + "&key=sk-x")
		if err != nil || p.Chat != w[0] || p.ID != id || !p.IsOpenCode() {
			t.Errorf("%s import: %+v %v", id, p, err)
		}
	}
}

func TestAcceptIsOpenCode(t *testing.T) {
	for base, want := range map[string]bool{
		"https://opencode.ai/zen/go/v1": true,
		"https://opencode.ai/zen":       true,
		"https://api.opencode.ai/v1":    true,
		"https://notopencode.ai/v1":     false,
		"https://opencode.ai.evil.com":  false,
		"https://api.deepseek.com/v1":   false,
	} {
		if got := (provider.Provider{Chat: base}).IsOpenCode(); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
}

func TestAcceptImportPreset(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek&key=sk-abc123&models=deepseek-v4-pro,%20,deepseek-v4-flash,")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("deepseek")
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc123" || p.Chat != pr.Chat || p.Name != pr.Name {
		t.Fatalf("got %+v", p)
	}
	if strings.Join(p.Models, "|") != "deepseek-v4-pro|deepseek-v4-flash" {
		t.Fatalf("models %q", p.Models)
	}
	p, err = provider.ParseImport("magpie://import?preset=deepseek&name=Mine&id=my-ds")
	if err != nil || p.Name != "Mine" || p.ID != "my-ds" || p.Chat != pr.Chat || p.Key != "" {
		t.Fatalf("override: %+v %v", p, err)
	}
}

func TestAcceptImportRegion(t *testing.T) {
	for _, pr := range provider.Presets() {
		if len(pr.Regions) < 2 {
			continue
		}
		r := pr.Regions[1]
		p, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=" + url.QueryEscape(r.ID))
		if err != nil {
			t.Fatal(err)
		}
		if p.Chat != r.Chat || p.Anthropic != r.Anthropic {
			t.Errorf("%s: got %+v, want region %+v", pr.ID, p, r)
		}
		if _, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=nowhere"); err == nil {
			t.Error("unknown region accepted")
		}
		return
	}
	t.Skip("no preset with regions")
}

func TestAcceptImportCustom(t *testing.T) {
	q := url.Values{
		"name":      {"My Relay"},
		"chat":      {"https://relay.example/v1/"},
		"responses": {"https://relay.example/v1"},
		"anthropic": {"https://relay.example"},
		"key":       {"sk-relay"},
		"website":   {"https://relay.example"},
		"keys":      {"http://relay.example/keys"},
		"catalog":   {"openai"},
		"models":    {"a, b ,c"},
	}
	p, err := provider.ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" || p.Chat != "https://relay.example/v1" || p.Responses != "https://relay.example/v1" || p.Anthropic != "https://relay.example" || p.Preset != "" || p.Key != "sk-relay" {
		t.Fatalf("got %+v", p)
	}
	if p.Website != "https://relay.example" || p.KeysURL != "" {
		t.Fatalf("pages: %q %q", p.Website, p.KeysURL)
	}
	if strings.Join(p.Models, "|") != "a|b|c" {
		t.Fatalf("models %q", p.Models)
	}
	// an explicit id wins over the one derived from the name
	q.Set("id", "relay2")
	if p, err = provider.ParseImport("magpie://import?" + q.Encode()); err != nil || p.ID != "relay2" {
		t.Fatalf("id: %+v %v", p, err)
	}
	// only an Anthropic endpoint is enough
	if p, err = provider.ParseImport("magpie://import?name=A&anthropic=https://a.example/"); err != nil || p.Anthropic != "https://a.example" {
		t.Fatalf("anthropic only: %+v %v", p, err)
	}
}

func TestAcceptImportLimits(t *testing.T) {
	long := strings.Repeat("n", 100)
	p, err := provider.ParseImport("magpie://import?name=" + long + "&chat=https://x.example/v1")
	if err != nil || len(p.Name) != 80 {
		t.Fatalf("long name: %d %v", len(p.Name), err)
	}
	if _, err := provider.ParseImport("magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4096)); err != nil {
		t.Errorf("4096-char key refused: %v", err)
	}
	if _, err := provider.ParseImport("magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097)); err == nil {
		t.Error("4097-char key accepted")
	}
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	if p, err := provider.ParseImport("magpie://import?preset=deepseek&models=" + strings.Join(ms, ",")); err == nil && len(p.Models) != 200 {
		t.Errorf("models: %d", len(p.Models))
	}
}

func TestAcceptImportRejects(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"http://import?preset=deepseek",
		"magpie://delete?id=deepseek",
		"magpie://import",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?chat=https://x.example/v1",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=http://8.8.8.8/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=https://relay.example/v1?a=b",
		"magpie://import?name=X&chat=https://relay.example/v1%23frag",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=X&chat=ftp://relay.example/v1",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=X&id=magpie&chat=https://x.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://x.example/v1",
		"magpie://import?preset=deepseek&key=sk%0Aevil",
		"magpie://import?preset=deepseek&key=sk%00evil",
	} {
		if p, err := provider.ParseImport(link); err == nil {
			t.Errorf("%s: accepted as %+v", link, p)
		}
	}
}

func TestAcceptImportLocalHTTPAndForms(t *testing.T) {
	for _, host := range []string{"localhost:11434", "127.0.0.1:8080", "192.168.1.20:8000", "10.0.0.5", "box.local"} {
		p, err := provider.ParseImport("magpie://import?name=Local&chat=http://" + host + "/v1")
		if err != nil || p.Chat != "http://"+host+"/v1" {
			t.Errorf("%s: %+v %v", host, p, err)
		}
	}
	for _, link := range []string{
		"magpie:import?preset=deepseek", "magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek", "Magpie://Import?preset=deepseek", "magpie://import/?preset=deepseek",
	} {
		p, err := provider.ParseImport(link)
		if err != nil || p.ID != "deepseek" {
			t.Errorf("%s: %+v %v", link, p, err)
		}
	}
}

func TestAcceptImportSavesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := provider.ParseImport("magpie://import?preset=deepseek&key=sk-secret"); err != nil {
		t.Fatal(err)
	}
	for _, p := range provider.All() {
		if p.ID == "deepseek" {
			t.Fatalf("ParseImport saved %+v", p)
		}
	}
}
