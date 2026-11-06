package provider

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): unexpected error: %v", link, err)
	}
	return p
}

func TestSchemeConst(t *testing.T) {
	if Scheme != "magpie" {
		t.Fatalf("Scheme = %q", Scheme)
	}
}

func TestImportCustomRelay(t *testing.T) {
	p := mustParse(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&models=a")
	if p.ID != "my-relay" {
		t.Errorf("id = %q, want my-relay", p.ID)
	}
	if p.Name != "My Relay" {
		t.Errorf("name = %q", p.Name)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("chat = %q, want trailing slash removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Errorf("anthropic = %q", p.Anthropic)
	}
	if len(p.Models) != 1 || p.Models[0] != "a" {
		t.Errorf("models = %v", p.Models)
	}
	if p.Preset != "" {
		t.Errorf("preset = %q, want empty", p.Preset)
	}
}

func TestImportWebsiteAndKeys(t *testing.T) {
	p := mustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&website=https://relay.example&keys=javascript:alert(1)")
	if p.Website != "https://relay.example" {
		t.Errorf("website = %q, want kept", p.Website)
	}
	if p.KeysURL != "" {
		t.Errorf("keys = %q, want ignored", p.KeysURL)
	}
}

func TestImportCatalogValidation(t *testing.T) {
	p := mustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&catalog=openai")
	if p.Catalog != "openai" {
		t.Errorf("catalog = %q, want openai", p.Catalog)
	}
	p = mustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&catalog=../etc")
	if p.Catalog != "" {
		t.Errorf("catalog = %q, want ignored", p.Catalog)
	}
}

func TestImportPresetDeepSeek(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if p.ID != "deepseek" {
		t.Errorf("id = %q", p.ID)
	}
	if p.Key != "sk-abc" {
		t.Errorf("key = %q", p.Key)
	}
	if len(p.Models) != 2 || p.Models[0] != "a" || p.Models[1] != "b" {
		t.Errorf("models = %v, want [a b]", p.Models)
	}
	if p.Preset != "deepseek" || p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("preset endpoints not taken: preset=%q chat=%q", p.Preset, p.Chat)
	}
}

func TestImportPresetIgnoresEndpointParams(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&chat=https://evil.example/v1&keys=https://evil.example")
	if p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("chat = %q, preset endpoint should win", p.Chat)
	}
	if p.KeysURL == "https://evil.example" {
		t.Errorf("keys taken from link with preset")
	}
}

func TestImportPresetRegion(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=yylx&region=cn")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn endpoints: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	if p.ID != "yylx" {
		t.Errorf("id = %q", p.ID)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Error("region=nowhere accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Error("region on a preset without regions accepted")
	}
}

func TestImportPresetNameOverride(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&name=Work%20DS&id=ds-work")
	if p.Name != "Work DS" || p.ID != "ds-work" {
		t.Errorf("name=%q id=%q", p.Name, p.ID)
	}
}

func TestImportRejects(t *testing.T) {
	cases := map[string]string{
		"https web link":        "https://usemagpie.ai/import?preset=deepseek",
		"other action":          "magpie://delete?id=x",
		"no params":             "magpie://import",
		"unknown preset":        "magpie://import?preset=nope",
		"name without base":     "magpie://import?name=Relay",
		"http public chat":      "magpie://import?name=R&chat=http://relay.example/v1",
		"credentials":           "magpie://import?name=R&chat=https://user:pw@relay.example/v1",
		"file scheme":           "magpie://import?name=R&chat=file:///etc/passwd",
		"reserved name":         "magpie://import?name=magpie",
		"empty slug":            "magpie://import?name=%E2%80%94",
		"control char in key":   "magpie://import?preset=deepseek&key=sk%0A",
		"query on base":         "magpie://import?name=R&chat=https://relay.example/v1?x=1",
		"fragment on base":      "magpie://import?name=R&chat=https://relay.example/v1%23x",
		"fragment on link":      "magpie://import?preset=deepseek#x",
		"key too long":          "magpie://import?preset=deepseek&key=" + strings.Repeat("a", 4097),
		"no name nor preset":    "magpie://import?chat=https://relay.example/v1",
		"region without preset": "magpie://import?name=R&chat=https://relay.example/v1&region=cn",
		"bad escape":            "magpie://import?preset=deepseek&key=%zz",
		"duplicate param":       "magpie://import?preset=deepseek&key=a&key=b",
		"not magpie":            "http://import?preset=deepseek",
		"explicit id reserved":  "magpie://import?preset=deepseek&id=magpie",
	}
	for name, link := range cases {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("%s: ParseImport(%q) accepted, want error", name, link)
		}
	}
}

func TestImportAcceptsLocalHTTP(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://box.local/v1",
		"http://10.1.2.3/v1",
		"http://172.16.0.9/v1",
		"http://[::1]:8000/v1",
	} {
		p, err := ParseImport("magpie://import?name=Local&chat=" + base)
		if err != nil {
			t.Errorf("chat=%s rejected: %v", base, err)
			continue
		}
		if p.Chat != base {
			t.Errorf("chat = %q, want %q", p.Chat, base)
		}
	}
}

func TestImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"magpie://IMPORT?preset=deepseek",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%q rejected: %v", link, err)
			continue
		}
		if p.ID != "deepseek" {
			t.Errorf("%q: id = %q", link, p.ID)
		}
	}
}

func TestImportNameTruncatedAndModelsCapped(t *testing.T) {
	long := strings.Repeat("n", 100)
	p := mustParse(t, "magpie://import?name="+long+"&chat=https://relay.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Errorf("name length = %d, want 80", len([]rune(p.Name)))
	}
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	p = mustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&models="+strings.Join(ms, ","))
	if len(p.Models) != 200 {
		t.Errorf("models = %d, want 200", len(p.Models))
	}
}

func TestImportKeyBoundary(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&key="+strings.Repeat("a", 4096))
	if len(p.Key) != 4096 {
		t.Errorf("key length = %d", len(p.Key))
	}
}
