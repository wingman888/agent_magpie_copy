package provider

import (
	"strings"
	"testing"
)

func TestS1OpenCodePresets(t *testing.T) {
	cases := map[string]struct{ chat, responses, anthropic, website, keys string }{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go", "https://opencode.ai/auth"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen", "https://opencode.ai/auth"},
	}
	for id, want := range cases {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != want.chat || pr.Responses != want.responses || pr.Anthropic != want.anthropic {
			t.Errorf("%s endpoints = %q %q %q", id, pr.Chat, pr.Responses, pr.Anthropic)
		}
		if pr.Website != want.website || pr.KeysURL != want.keys {
			t.Errorf("%s website/keys = %q %q", id, pr.Website, pr.KeysURL)
		}
		p, err := FromPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsOpenCode() {
			t.Errorf("%s should be an OpenCode provider", id)
		}
	}
}

func TestS1IsOpenCode(t *testing.T) {
	yes := []Provider{
		{Chat: "https://opencode.ai/zen/v1"},
		{Anthropic: "https://OPENCODE.ai/zen"},
		{Responses: "https://gw.opencode.ai/v1"},
		{Chat: "https://opencode.ai:443/v1"},
	}
	no := []Provider{
		{Chat: "https://notopencode.ai/v1"},
		{Chat: "https://api.deepseek.com/v1"},
		{Chat: "https://opencode.ai.evil.example/v1"},
		{},
	}
	for _, p := range yes {
		if !p.IsOpenCode() {
			t.Errorf("IsOpenCode(%+v) = false, want true", p)
		}
	}
	for _, p := range no {
		if p.IsOpenCode() {
			t.Errorf("IsOpenCode(%+v) = true, want false", p)
		}
	}
}

func TestS1ParseImportAccepted(t *testing.T) {
	links := []string{
		"magpie://import?preset=deepseek&key=sk-1",
		"magpie:import?preset=deepseek&key=sk-1",
		"magpie:///import?preset=deepseek&key=sk-1",
		"MAGPIE://import?preset=deepseek&key=sk-1",
		"Magpie:import/?preset=deepseek&key=sk-1",
	}
	for _, l := range links {
		p, err := ParseImport(l)
		if err != nil {
			t.Errorf("ParseImport(%q): %v", l, err)
			continue
		}
		if p.ID != "deepseek" || p.Key != "sk-1" || p.Chat != "https://api.deepseek.com/v1" {
			t.Errorf("ParseImport(%q) = %+v", l, p)
		}
	}

	p, err := ParseImport("magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/&key=sk-x&models=a, ,b,,a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Acme Relay" || p.ID != "acme-relay" || p.Chat != "https://api.acme.example/v1" {
		t.Errorf("custom import = %+v", p)
	}
	if strings.Join(p.Models, ",") != "a,b" {
		t.Errorf("models = %v", p.Models)
	}
	if p.Preset != "" {
		t.Errorf("custom import should have no preset, got %q", p.Preset)
	}

	p, err = ParseImport("magpie://import?preset=yylx&region=cn")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "yylx" || p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region import = %+v", p)
	}
	p, err = ParseImport("magpie://import?preset=yylx")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://app.yylx.io/v1" {
		t.Errorf("default region import = %+v", p)
	}

	p, err = ParseImport("magpie://import?name=Mine&chat=http://192.168.1.5:8000/v1&anthropic=http://localhost:9000")
	if err != nil {
		t.Errorf("private/local http should be accepted: %v", err)
	}
	p, err = ParseImport("magpie://import?name=Mine&chat=https://x.example/v1&website=http://x.example&keys=https://x.example/keys")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "" || p.KeysURL != "https://x.example/keys" {
		t.Errorf("website/keys filtering: website=%q keys=%q", p.Website, p.KeysURL)
	}

	long := strings.Repeat("n", 100)
	p, err = ParseImport("magpie://import?name=" + long + "&chat=https://x.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(p.Name)) != 80 {
		t.Errorf("name not truncated to 80: %d", len([]rune(p.Name)))
	}

	p, err = ParseImport("magpie://import?preset=opencode-go&key=sk-o")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsOpenCode() || p.ID != "opencode-go" {
		t.Errorf("opencode-go import = %+v", p)
	}
}

func TestS1ParseImportRejected(t *testing.T) {
	bad := []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?preset=deepseek",
		"magpie://import?name=X",
		"magpie://import?chat=https://x.example/v1",
		"magpie://import?name=X&chat=http://api.example.com/v1",
		"magpie://import?name=X&chat=https://u:p@x.example/v1",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=X&chat=https://x.example/v1?a=b",
		"magpie://import?name=X&chat=https://x.example/v1%23f",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=!!!&chat=https://x.example/v1",
		"magpie://import?name=X&id=Acme&chat=https://x.example/v1",
		"magpie://import?name=X&chat=https://x.example/v1&key=a%0Ab",
		"magpie://import?name=X&chat=https://x.example/v1&key=" + strings.Repeat("k", 4097),
		"magpie://import?preset=nope&key=k",
		"magpie://import?preset=yylx&region=mars",
		"magpie://import?preset=deepseek&region=cn",
		"magpie://import?name=X&chat=https://x.example/v1&models=" + strings.TrimSuffix(strings.Repeat("m,", 201), ","),
		"magpie://import?name=X&chat=https://x.example/v1&chat=https://y.example/v1",
		"",
	}
	for _, l := range bad {
		if p, err := ParseImport(l); err == nil {
			t.Errorf("ParseImport(%q) accepted: %+v", l, p)
		}
	}
}

func TestS1ExcludedReadsStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Excluded(); len(got) != 0 {
		t.Fatalf("empty store: Excluded() = %+v", got)
	}
	if err := store(file{Excluded: []Exclusion{{Agent: "codex", Provider: "codex", Why: "removed by user"}}}); err != nil {
		t.Fatal(err)
	}
	got := Excluded()
	if len(got) != 1 || got[0].Agent != "codex" || got[0].Provider != "codex" || got[0].Why != "removed by user" {
		t.Fatalf("Excluded() = %+v", got)
	}
}
