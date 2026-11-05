package provider

import (
	"strconv"
	"strings"
	"testing"
)

func TestImportParsePresetLink(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" {
		t.Errorf("id/preset = %q/%q, want deepseek", p.ID, p.Preset)
	}
	if p.Key != "sk-abc" {
		t.Errorf("key = %q, want sk-abc", p.Key)
	}
	if p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("chat = %q, want the preset endpoint", p.Chat)
	}
	if strings.Join(p.Models, ",") != "a,b" {
		t.Errorf("models = %v, want [a b]", p.Models)
	}
}

func TestImportParseSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  magpie://import?preset=deepseek  ",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", link, err)
			continue
		}
		if p.ID != "deepseek" {
			t.Errorf("%q: id = %q, want deepseek", link, p.ID)
		}
	}
}

func TestImportParseRejectsBadLinks(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?",
		"magpie://import?preset=nope",
		"magpie://import?name=Only%20A%20Name",
		"magpie://import?name=Relay&chat=http://relay.example/v1",
		"magpie://import?name=Relay&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=Relay&chat=file:///etc/passwd",
		"magpie://import?name=Relay&chat=https://relay.example/v1?x=1",
		"magpie://import?name=Relay&chat=https://relay.example/v1#frag",
		"magpie://import?name=magpie&chat=https://relay.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://relay.example/v1",
		"magpie://import?preset=deepseek&key=sk-abc%0A",
		"magpie://import?preset=deepseek&key=sk%0Dabc",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097),
		"magpie://import?preset=deepseek&bogus=1",
		"magpie://import?preset=deepseek&preset=openai",
		"magpie://import?preset=deepseek&region=cn",
		"magpie://import?name=Relay&chat=https://relay.example/v1&models=" + importTestManyModels(201),
	} {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%q: expected an error, got %+v", link, p)
		}
	}
}

func TestImportParseCustomLink(t *testing.T) {
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != "my-relay" {
		t.Errorf("id = %q, want my-relay", p.ID)
	}
	if p.Name != "My Relay" {
		t.Errorf("name = %q, want My Relay", p.Name)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("chat = %q, want trailing slash removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Errorf("anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Errorf("website = %q, want https kept", p.Website)
	}
	if p.KeysURL != "" {
		t.Errorf("keys = %q, want javascript: ignored", p.KeysURL)
	}
	if p.Preset != "" {
		t.Errorf("preset = %q, want empty for custom", p.Preset)
	}
}

func TestImportParseExplicitIDAndCatalog(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme&id=Acme_Two&chat=https://api.acme.example/v1&catalog=Bad%20Value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != "acme-two" {
		t.Errorf("id = %q, want acme-two", p.ID)
	}
	if p.Catalog != "" {
		t.Errorf("catalog = %q, want invalid value ignored", p.Catalog)
	}
	p, err = ParseImport("magpie://import?name=Acme&chat=https://api.acme.example/v1&catalog=moonshot_ai-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Catalog != "moonshot_ai-2" {
		t.Errorf("catalog = %q, want moonshot_ai-2", p.Catalog)
	}
}

func TestImportParseRegion(t *testing.T) {
	pr := Preset("yylx")
	if pr == nil || len(pr.Regions) < 3 {
		t.Fatalf("yylx preset with regions is required for this test")
	}
	p, err := ParseImport("magpie://import?preset=yylx&region=cn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("endpoints = %q / %q, want the cn region", p.Chat, p.Anthropic)
	}
	if p.ID != "yylx" {
		t.Errorf("id = %q, want yylx", p.ID)
	}

	p, err = ParseImport("magpie://import?preset=yylx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Chat != pr.Regions[0].Chat {
		t.Errorf("default endpoint = %q, want first region %q", p.Chat, pr.Regions[0].Chat)
	}

	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Errorf("region=nowhere: expected an error")
	}
}

func TestImportParsePresetOverridesAndIgnores(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=openai&name=My%20OpenAI&id=oa&chat=https://evil.example/v1&website=https://evil.example&keys=https://evil.example&catalog=other")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "My OpenAI" || p.ID != "oa" {
		t.Errorf("name/id = %q/%q", p.Name, p.ID)
	}
	if p.Chat != "https://api.openai.com/v1" {
		t.Errorf("chat = %q, preset endpoint must win", p.Chat)
	}
	if p.Website != "https://platform.openai.com" || p.KeysURL != "https://platform.openai.com/api-keys" {
		t.Errorf("website/keys = %q / %q, preset values must win", p.Website, p.KeysURL)
	}
	if p.Catalog != "openai" {
		t.Errorf("catalog = %q, want preset catalog", p.Catalog)
	}
}

func TestImportParseLocalHTTP(t *testing.T) {
	for _, chat := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.16.4.4/v1",
		"http://box.local/v1",
		"http://[::1]:8080/v1",
	} {
		p, err := ParseImport("magpie://import?name=Local&chat=" + importTestEscape(chat))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", chat, err)
			continue
		}
		if p.Chat != chat {
			t.Errorf("%s: chat = %q", chat, p.Chat)
		}
	}
	for _, chat := range []string{
		"http://relay.example/v1",
		"http://8.8.8.8/v1",
		"http://172.32.0.1/v1",
	} {
		if _, err := ParseImport("magpie://import?name=Remote&chat=" + importTestEscape(chat)); err == nil {
			t.Errorf("%s: expected http to be rejected", chat)
		}
	}
}

func TestImportParseKeyAndNameEdges(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1&key=" + strings.Repeat("k", 4096))
	if err != nil {
		t.Fatalf("key of exactly 4096 bytes: unexpected error: %v", err)
	}
	if len(p.Key) != 4096 {
		t.Errorf("key length = %d", len(p.Key))
	}

	p, err = ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Key != "" {
		t.Errorf("key = %q, want empty when absent", p.Key)
	}

	long := strings.Repeat("a", 81)
	p, err = ParseImport("magpie://import?name=" + long + "&chat=https://relay.example/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := len([]rune(p.Name)); n != 80 {
		t.Errorf("name length = %d, want 80", n)
	}

	if _, err := ParseImport("magpie://import?name=%E2%80%94&id=ok&chat=https://relay.example/v1"); err != nil {
		t.Errorf("explicit id should be accepted even when name has no slug: %v", err)
	}
	if _, err := ParseImport("magpie://import?name=Relay&id=magpie&chat=https://relay.example/v1"); err == nil {
		t.Errorf("explicit id=magpie: expected an error")
	}
	if _, err := ParseImport("magpie://import?name=Relay&id=%E2%80%94&chat=https://relay.example/v1"); err == nil {
		t.Errorf("explicit id that normalises to empty: expected an error")
	}
}

func TestImportParseModels(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1&models=" + "%20a%20,,%20%20,b,,c%20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(p.Models, "|") != "a|b|c" {
		t.Errorf("models = %q, want a|b|c", p.Models)
	}

	p, err = ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1&models=" + importTestManyModels(200))
	if err != nil {
		t.Fatalf("200 models: unexpected error: %v", err)
	}
	if len(p.Models) != 200 {
		t.Errorf("models = %d, want 200", len(p.Models))
	}
}

func TestImportParseRequiresBaseURL(t *testing.T) {
	if _, err := ParseImport("magpie://import?name=Relay&catalog=openai"); err == nil {
		t.Errorf("custom link without base URL: expected an error")
	}
}

// importTestManyModels returns n comma-separated distinct model ids.
func importTestManyModels(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "m" + strconv.Itoa(i)
	}
	return strings.Join(parts, ",")
}

// importTestEscape escapes the characters that matter in a query value.
func importTestEscape(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F", "[", "%5B", "]", "%5D").Replace(s)
}
