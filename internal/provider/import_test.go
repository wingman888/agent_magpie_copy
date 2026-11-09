package provider

import (
	"strings"
	"testing"
)

// impTestMustParse parses a link that must be accepted.
func impTestMustParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q) rejected: %v", link, err)
	}
	return p
}

// impTestMustReject checks that a link is refused.
func impTestMustReject(t *testing.T, link string) {
	t.Helper()
	if p, err := ParseImport(link); err == nil {
		t.Fatalf("ParseImport(%q) accepted, got %+v", link, p)
	}
}

func TestImportSchemeConstant(t *testing.T) {
	if Scheme != "magpie" {
		t.Fatalf("Scheme = %q", Scheme)
	}
}

func TestImportPresetLink(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if p.ID != "deepseek" || p.Key != "sk-abc" || p.Preset != "deepseek" {
		t.Fatalf("got id=%q key=%q preset=%q", p.ID, p.Key, p.Preset)
	}
	if len(p.Models) != 2 || p.Models[0] != "a" || p.Models[1] != "b" {
		t.Fatalf("models = %q", p.Models)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Fatalf("endpoints from preset not used: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
}

func TestImportPresetWithoutKeyAndOverrides(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?preset=deepseek&name=Ds%20Work&id=ds-work&website=javascript:alert(1)")
	if p.Key != "" {
		t.Fatalf("key = %q, want empty", p.Key)
	}
	if p.Name != "Ds Work" || p.ID != "ds-work" {
		t.Fatalf("name=%q id=%q", p.Name, p.ID)
	}
	if p.Website != "https://platform.deepseek.com" {
		t.Fatalf("preset website should stand, got %q", p.Website)
	}
}

func TestImportPresetRegion(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?preset=yylx&region=global&key=k")
	if p.Chat != "https://global.yylx.io/v1" || p.Anthropic != "https://global.yylx.io" {
		t.Fatalf("region endpoints not used: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	impTestMustReject(t, "magpie://import?preset=yylx&region=nowhere")
	impTestMustReject(t, "magpie://import?preset=deepseek&region=global")
}

func TestImportCustomLink(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)&key=sk-x")
	if p.ID != "my-relay" {
		t.Fatalf("id = %q", p.ID)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Fatalf("chat = %q, trailing slash should be removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Fatalf("anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Fatalf("website = %q, https should be kept", p.Website)
	}
	if p.KeysURL != "" {
		t.Fatalf("keys = %q, javascript: should be ignored", p.KeysURL)
	}
	if p.Preset != "" || p.Key != "sk-x" {
		t.Fatalf("preset=%q key=%q", p.Preset, p.Key)
	}
}

func TestImportCustomCatalogAndExplicitID(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?name=Acme&id=Acme%20Relay&responses=https://a.example/v1&catalog=openai")
	if p.ID != "acme-relay" || p.Catalog != "openai" {
		t.Fatalf("id=%q catalog=%q", p.ID, p.Catalog)
	}
	p = impTestMustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&catalog=Not+Valid")
	if p.Catalog != "" {
		t.Fatalf("invalid catalog should be ignored, got %q", p.Catalog)
	}
}

func TestImportModelsCleanedAndCapped(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?name=R&chat=https://a.example/v1&models=%20x%20,,y,x,")
	if len(p.Models) != 2 || p.Models[0] != "x" || p.Models[1] != "y" {
		t.Fatalf("models = %q", p.Models)
	}
	var many []string
	for i := 0; i < impMaxModels+50; i++ {
		many = append(many, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("0", i/26))
	}
	p = impTestMustParse(t, "magpie://import?name=R&chat=https://a.example/v1&models="+strings.Join(many, ","))
	if len(p.Models) != impMaxModels {
		t.Fatalf("len(models) = %d, want %d", len(p.Models), impMaxModels)
	}
}

func TestImportNameTruncated(t *testing.T) {
	long := strings.Repeat("n", 100)
	p := impTestMustParse(t, "magpie://import?name="+long+"&chat=https://a.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Fatalf("name length = %d, want 80", len([]rune(p.Name)))
	}
}

func TestImportLinkSpellings(t *testing.T) {
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"Magpie:Import?preset=deepseek",
	} {
		p := impTestMustParse(t, link)
		if p.ID != "deepseek" {
			t.Fatalf("ParseImport(%q) id = %q", link, p.ID)
		}
	}
}

func TestImportLocalHTTPAccepted(t *testing.T) {
	for _, chat := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://box.local/v1",
	} {
		p := impTestMustParse(t, "magpie://import?name=Local&chat="+chat)
		if p.Chat != chat {
			t.Fatalf("chat = %q, want %q", p.Chat, chat)
		}
	}
}

func TestImportRejected(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?",
		"magpie://import?preset=nope",
		"magpie://import?name=Only",
		"magpie://import?name=R&chat=http://relay.example/v1",
		"magpie://import?name=R&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=R&chat=file:///etc/passwd",
		"magpie://import?name=R&chat=https://relay.example/v1?x=1",
		"magpie://import?name=R&chat=https://relay.example/v1#frag",
		"magpie://import?name=magpie&chat=https://relay.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://relay.example/v1",
		"magpie://import?name=R&chat=https://relay.example/v1&key=sk-a%0A",
		"magpie://import?name=R&chat=https://relay.example/v1&key=" + strings.Repeat("k", 4097),
		"magpie://import?preset=deepseek&bogus=1",
		"magpie://import?preset=deepseek&preset=openai",
		"magpie://import?preset=deepseek&key=%zz",
		"magpie://import?name=R&id=magpie&chat=https://relay.example/v1",
		"magpie://import?name=R&id=%E2%80%94&chat=https://relay.example/v1",
		"magpie://import?name=R&anthropic=http://0.0.0.0:8000",
	} {
		impTestMustReject(t, link)
	}
}

func TestImportKeyAtLimitAccepted(t *testing.T) {
	key := strings.Repeat("k", impMaxKey)
	p := impTestMustParse(t, "magpie://import?name=R&chat=https://relay.example/v1&key="+key)
	if p.Key != key {
		t.Fatalf("key length = %d", len(p.Key))
	}
}
