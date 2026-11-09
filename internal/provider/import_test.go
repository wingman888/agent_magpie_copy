package provider

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// importTestMust parses a link and fails the test when it is rejected.
func importTestMust(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): unexpected error: %v", link, err)
	}
	return p
}

// importTestRejects asserts that a link is rejected.
func importTestRejects(t *testing.T, link string) {
	t.Helper()
	if p, err := ParseImport(link); err == nil {
		t.Errorf("ParseImport(%q) accepted it: %+v", link, p)
	}
}

func TestImportLinkScheme(t *testing.T) {
	if Scheme != "magpie" {
		t.Fatalf("Scheme = %q", Scheme)
	}
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  magpie://import?preset=deepseek  ",
	} {
		if p := importTestMust(t, link); p.ID != "deepseek" {
			t.Errorf("%q: id = %q, want deepseek", link, p.ID)
		}
	}
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import/extra?preset=deepseek",
		"magpie://import#preset=deepseek",
		"deepseek",
	} {
		importTestRejects(t, link)
	}
}

func TestImportLinkPresetFlow(t *testing.T) {
	p := importTestMust(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if p.ID != "deepseek" || p.Preset != "deepseek" {
		t.Errorf("id/preset = %q/%q", p.ID, p.Preset)
	}
	if p.Key != "sk-abc" {
		t.Errorf("key = %q", p.Key)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("endpoints = chat %q anthropic %q", p.Chat, p.Anthropic)
	}
	if p.Name != "DeepSeek" || p.Catalog != "deepseek" {
		t.Errorf("name/catalog = %q/%q", p.Name, p.Catalog)
	}
	if strings.Join(p.Models, ",") != "a,b" {
		t.Errorf("models = %q", p.Models)
	}
}

func TestImportLinkPresetIgnoresEndpointFields(t *testing.T) {
	p := importTestMust(t, "magpie://import?preset=deepseek&chat=https://evil.example/v1&anthropic=https://evil.example&catalog=../x&keys=javascript:alert(1)")
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("preset endpoints overridden: chat %q anthropic %q", p.Chat, p.Anthropic)
	}
	if p.Catalog != "deepseek" {
		t.Errorf("catalog = %q", p.Catalog)
	}
	if p.KeysURL != "https://platform.deepseek.com/api_keys" {
		t.Errorf("keys url = %q", p.KeysURL)
	}
}

func TestImportLinkPresetNameAndID(t *testing.T) {
	p := importTestMust(t, "magpie://import?preset=deepseek&name=My%20DS")
	if p.Name != "My DS" || p.ID != "deepseek" {
		t.Errorf("name/id = %q/%q, want My DS/deepseek", p.Name, p.ID)
	}
	p = importTestMust(t, "magpie://import?preset=deepseek&id=ds-work")
	if p.ID != "ds-work" {
		t.Errorf("explicit id = %q", p.ID)
	}
}

func TestImportLinkRegions(t *testing.T) {
	p := importTestMust(t, "magpie://import?preset=yylx&region=cn")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn endpoints: chat %q anthropic %q", p.Chat, p.Anthropic)
	}
	if p.Responses != "" {
		t.Errorf("region cn responses = %q, want empty", p.Responses)
	}
	importTestRejects(t, "magpie://import?preset=yylx&region=nowhere")
	importTestRejects(t, "magpie://import?preset=deepseek&region=cn")
}

func TestImportLinkCustom(t *testing.T) {
	p := importTestMust(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)")
	if p.ID != "my-relay" || p.Name != "My Relay" {
		t.Errorf("id/name = %q/%q", p.ID, p.Name)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("chat = %q, want trailing slash removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Errorf("anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Errorf("website = %q", p.Website)
	}
	if p.KeysURL != "" {
		t.Errorf("keys url = %q, want ignored", p.KeysURL)
	}
	if p.Preset != "" || p.Key != "" || p.Models != nil {
		t.Errorf("unexpected fields: %+v", p)
	}
}

func TestImportLinkCustomCatalog(t *testing.T) {
	p := importTestMust(t, "magpie://import?name=R&chat=https://r.example/v1&catalog=openai")
	if p.Catalog != "openai" {
		t.Errorf("catalog = %q", p.Catalog)
	}
	p = importTestMust(t, "magpie://import?name=R&chat=https://r.example/v1&catalog=Not+Valid%2F")
	if p.Catalog != "" {
		t.Errorf("invalid catalog kept: %q", p.Catalog)
	}
}

func TestImportLinkCustomNeedsNameAndBase(t *testing.T) {
	importTestRejects(t, "magpie://import?chat=https://relay.example/v1")
	importTestRejects(t, "magpie://import?name=OnlyName")
	importTestRejects(t, "magpie://import?name=OnlyName&website=https://x.example")
}

func TestImportLinkIDDerivation(t *testing.T) {
	p := importTestMust(t, "magpie://import?name=--Hello%20%20World!!&chat=https://h.example/v1")
	if p.ID != "hello-world" {
		t.Errorf("derived id = %q", p.ID)
	}
	p = importTestMust(t, "magpie://import?name=Acme&id=Acme%20Relay&chat=https://h.example/v1")
	if p.ID != "acme-relay" {
		t.Errorf("explicit id = %q", p.ID)
	}
}

func TestImportLinkReservedAndSymbolNames(t *testing.T) {
	importTestRejects(t, "magpie://import?name=magpie&chat=https://relay.example/v1")
	importTestRejects(t, "magpie://import?name=Magpie&chat=https://relay.example/v1")
	importTestRejects(t, "magpie://import?name=x&id=magpie&chat=https://relay.example/v1")
	importTestRejects(t, "magpie://import?name=%E2%80%94&chat=https://relay.example/v1")
	importTestRejects(t, "magpie://import?name=%E2%80%94&id=%E2%80%94&chat=https://relay.example/v1")
	p := importTestMust(t, "magpie://import?name=%E2%80%94&id=dash&chat=https://relay.example/v1")
	if p.ID != "dash" {
		t.Errorf("id = %q", p.ID)
	}
}

func TestImportLinkNameTruncation(t *testing.T) {
	long := strings.Repeat("界", 100)
	p := importTestMust(t, "magpie://import?name="+long+"&id=x&chat=https://relay.example/v1")
	if got := len([]rune(p.Name)); got != 80 {
		t.Errorf("name runes = %d, want 80", got)
	}
}

func TestImportLinkKey(t *testing.T) {
	importTestRejects(t, "magpie://import?preset=deepseek&key=sk-abc%0A")
	importTestRejects(t, "magpie://import?preset=deepseek&key=%0Ask-abc")
	importTestRejects(t, "magpie://import?preset=deepseek&key=sk%09abc")
	importTestRejects(t, "magpie://import?preset=deepseek&key="+strings.Repeat("k", 4097))
	p := importTestMust(t, "magpie://import?preset=deepseek&key="+strings.Repeat("k", 4096))
	if len(p.Key) != 4096 {
		t.Errorf("key length = %d", len(p.Key))
	}
	if p := importTestMust(t, "magpie://import?preset=deepseek"); p.Key != "" {
		t.Errorf("missing key = %q, want empty", p.Key)
	}
}

func TestImportLinkRejectedBaseURLs(t *testing.T) {
	for _, chat := range []string{
		"http://relay.example/v1",
		"https://user:pw@relay.example/v1",
		"https://relay.example/v1?x=1",
		"https://relay.example/v1#frag",
		"file:///etc/passwd",
		"relay.example/v1",
		"ftp://relay.example/v1",
	} {
		importTestRejects(t, "magpie://import?name=R&chat="+url.QueryEscape(chat))
	}
}

func TestImportLinkLocalHTTPAllowed(t *testing.T) {
	for _, chat := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://[::1]:9000/v1",
		"http://box.local/v1",
	} {
		p := importTestMust(t, "magpie://import?name=L&chat="+url.QueryEscape(chat))
		if p.Chat != strings.TrimRight(chat, "/") {
			t.Errorf("chat = %q, want %q", p.Chat, chat)
		}
	}
}

func TestImportLinkModels(t *testing.T) {
	p := importTestMust(t, "magpie://import?preset=deepseek&models=,%20a%20,,b,a,%20")
	if strings.Join(p.Models, "|") != "a|b" {
		t.Errorf("models = %q", p.Models)
	}
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, fmt.Sprintf("m%d", i))
	}
	importTestMust(t, "magpie://import?preset=deepseek&models="+strings.Join(many, ","))
	many = append(many, "m200")
	importTestRejects(t, "magpie://import?preset=deepseek&models="+strings.Join(many, ","))
}

func TestImportLinkMissingPresetAndDuplicates(t *testing.T) {
	importTestRejects(t, "magpie://import?preset=nope")
	importTestRejects(t, "magpie://import?preset=deepseek&preset=openai")
}
