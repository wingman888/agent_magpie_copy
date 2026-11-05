package provider

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q) unexpected error: %v", link, err)
	}
	return p
}

func TestScheme(t *testing.T) {
	if Scheme != "magpie" {
		t.Fatalf("Scheme = %q", Scheme)
	}
}

func TestImportPreset(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if p.ID != "deepseek" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.Key != "sk-abc" {
		t.Errorf("Key = %q", p.Key)
	}
	if !reflect.DeepEqual(p.Models, []string{"a", "b"}) {
		t.Errorf("Models = %v", p.Models)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("endpoints not from preset: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	if p.Preset != "deepseek" || p.Name != "DeepSeek" {
		t.Errorf("Preset/Name = %q/%q", p.Preset, p.Name)
	}
}

func TestImportPresetIgnoresEndpointParams(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&chat=https://evil.example/v1&catalog=openai&website=https://evil.example")
	if p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("Chat overridden by link: %q", p.Chat)
	}
	if p.Catalog != "deepseek" || p.Website != "https://platform.deepseek.com" {
		t.Errorf("Catalog/Website overridden: %q %q", p.Catalog, p.Website)
	}
}

func TestImportPresetNameAndID(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&name=My%20DS&id=ds-work")
	if p.Name != "My DS" || p.ID != "ds-work" {
		t.Errorf("Name/ID = %q/%q", p.Name, p.ID)
	}
}

func TestImportCustom(t *testing.T) {
	p := mustParse(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)")
	if p.ID != "my-relay" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("Chat = %q, trailing slash not removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Errorf("Anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Errorf("Website = %q", p.Website)
	}
	if p.KeysURL != "" {
		t.Errorf("KeysURL = %q, want ignored", p.KeysURL)
	}
	if p.Preset != "" {
		t.Errorf("Preset = %q", p.Preset)
	}
}

func TestImportCustomIDExplicitAndCatalog(t *testing.T) {
	p := mustParse(t, "magpie://import?name=Acme&id=Acme%20Relay&responses=https://acme.example/v1&catalog=fireworks-ai")
	if p.ID != "acme-relay" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.Catalog != "fireworks-ai" {
		t.Errorf("Catalog = %q", p.Catalog)
	}
	p = mustParse(t, "magpie://import?name=Acme&chat=https://acme.example/v1&catalog=../../etc")
	if p.Catalog != "" {
		t.Errorf("invalid catalog kept: %q", p.Catalog)
	}
}

func TestImportNameTruncated(t *testing.T) {
	long := strings.Repeat("n", 120)
	p := mustParse(t, "magpie://import?name="+long+"&chat=https://relay.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Errorf("name length = %d, want 80", len([]rune(p.Name)))
	}
}

func TestImportModelsLimit(t *testing.T) {
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", 1)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	p := mustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&models="+strings.Join(ms, ","))
	if len(p.Models) != 200 {
		t.Errorf("models = %d, want 200", len(p.Models))
	}
}

func TestImportRegion(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=yylx&region=cn")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn endpoints: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	p = mustParse(t, "magpie://import?preset=yylx")
	if p.Chat != "https://app.yylx.io/v1" {
		t.Errorf("default region endpoint: %q", p.Chat)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Error("region=nowhere accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Error("region on preset without regions accepted")
	}
}

func TestImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"Magpie://IMPORT?preset=deepseek",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("ParseImport(%q): %v", link, err)
			continue
		}
		if p.ID != "deepseek" {
			t.Errorf("ParseImport(%q) ID = %q", link, p.ID)
		}
	}
}

func TestImportRejectedLinks(t *testing.T) {
	cases := map[string]string{
		"https non-magpie":   "https://usemagpie.ai/import?preset=deepseek",
		"other action":       "magpie://delete?id=x",
		"no params":          "magpie://import",
		"unknown preset":     "magpie://import?preset=nope",
		"name no base URL":   "magpie://import?name=Relay",
		"chat plain http":    "magpie://import?name=Relay&chat=http://relay.example/v1",
		"chat userinfo":      "magpie://import?name=Relay&chat=https://user:pw@relay.example/v1",
		"chat file scheme":   "magpie://import?name=Relay&chat=file:///etc/passwd",
		"chat with query":    "magpie://import?name=Relay&chat=https://relay.example/v1?x=1",
		"chat with fragment": "magpie://import?name=Relay&chat=https://relay.example/v1%23frag",
		"reserved name":      "magpie://import?name=magpie&chat=https://relay.example/v1",
		"symbol-only name":   "magpie://import?name=%E2%80%94&chat=https://relay.example/v1",
		"key with newline":   "magpie://import?preset=deepseek&key=sk-a%0Ab",
		"key too long":       "magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097),
		"reserved explicit":  "magpie://import?name=Relay&id=magpie&chat=https://relay.example/v1",
	}
	for label, link := range cases {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%s: ParseImport(%q) accepted: %+v", label, link, p)
		}
	}
}

func TestImportAcceptedLocalHTTP(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.20.1.1/v1",
		"http://[::1]:8080/v1",
		"http://box.local/v1",
	} {
		if _, err := ParseImport("magpie://import?name=Local&chat=" + base); err != nil {
			t.Errorf("chat=%s rejected: %v", base, err)
		}
	}
}

func TestImportNoKeyIsEmpty(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=openai")
	if p.Key != "" {
		t.Errorf("Key = %q", p.Key)
	}
}
