package provider

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// f07TestMust parses a link and fails the test when it is rejected.
func f07TestMust(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): unexpected error: %v", link, err)
	}
	return p
}

// f07TestRejects asserts that a link is rejected.
func f07TestRejects(t *testing.T, link string) {
	t.Helper()
	if p, err := ParseImport(link); err == nil {
		t.Errorf("ParseImport(%q) accepted it: %+v", link, p)
	}
}

func TestF07ImportLinkScheme(t *testing.T) {
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
		if p := f07TestMust(t, link); p.ID != "deepseek" {
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
		f07TestRejects(t, link)
	}
}

func TestF07ImportLinkPresetFlow(t *testing.T) {
	p := f07TestMust(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
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

func TestF07ImportLinkPresetIgnoresEndpointFields(t *testing.T) {
	p := f07TestMust(t, "magpie://import?preset=deepseek&chat=https://evil.example/v1&anthropic=https://evil.example&catalog=../x&keys=javascript:alert(1)")
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

func TestF07ImportLinkPresetNameAndID(t *testing.T) {
	p := f07TestMust(t, "magpie://import?preset=deepseek&name=My%20DS")
	if p.Name != "My DS" || p.ID != "deepseek" {
		t.Errorf("name/id = %q/%q, want My DS/deepseek", p.Name, p.ID)
	}
	p = f07TestMust(t, "magpie://import?preset=deepseek&id=ds-work")
	if p.ID != "ds-work" {
		t.Errorf("explicit id = %q", p.ID)
	}
}

func TestF07ImportLinkRegions(t *testing.T) {
	p := f07TestMust(t, "magpie://import?preset=yylx&region=cn")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn endpoints: chat %q anthropic %q", p.Chat, p.Anthropic)
	}
	if p.Responses != "" {
		t.Errorf("region cn responses = %q, want empty", p.Responses)
	}
	f07TestRejects(t, "magpie://import?preset=yylx&region=nowhere")
	f07TestRejects(t, "magpie://import?preset=deepseek&region=cn")
}

func TestF07ImportLinkCustom(t *testing.T) {
	p := f07TestMust(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)")
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

func TestF07ImportLinkCustomCatalog(t *testing.T) {
	p := f07TestMust(t, "magpie://import?name=R&chat=https://r.example/v1&catalog=openai")
	if p.Catalog != "openai" {
		t.Errorf("catalog = %q", p.Catalog)
	}
	p = f07TestMust(t, "magpie://import?name=R&chat=https://r.example/v1&catalog=Not+Valid%2F")
	if p.Catalog != "" {
		t.Errorf("invalid catalog kept: %q", p.Catalog)
	}
}

func TestF07ImportLinkCustomNeedsNameAndBase(t *testing.T) {
	f07TestRejects(t, "magpie://import?chat=https://relay.example/v1")
	f07TestRejects(t, "magpie://import?name=OnlyName")
	f07TestRejects(t, "magpie://import?name=OnlyName&website=https://x.example")
}

func TestF07ImportLinkIDDerivation(t *testing.T) {
	p := f07TestMust(t, "magpie://import?name=--Hello%20%20World!!&chat=https://h.example/v1")
	if p.ID != "hello-world" {
		t.Errorf("derived id = %q", p.ID)
	}
	p = f07TestMust(t, "magpie://import?name=Acme&id=Acme%20Relay&chat=https://h.example/v1")
	if p.ID != "acme-relay" {
		t.Errorf("explicit id = %q", p.ID)
	}
}

func TestF07ImportLinkReservedAndSymbolNames(t *testing.T) {
	f07TestRejects(t, "magpie://import?name=magpie&chat=https://relay.example/v1")
	f07TestRejects(t, "magpie://import?name=Magpie&chat=https://relay.example/v1")
	f07TestRejects(t, "magpie://import?name=x&id=magpie&chat=https://relay.example/v1")
	f07TestRejects(t, "magpie://import?name=%E2%80%94&chat=https://relay.example/v1")
	f07TestRejects(t, "magpie://import?name=%E2%80%94&id=%E2%80%94&chat=https://relay.example/v1")
	p := f07TestMust(t, "magpie://import?name=%E2%80%94&id=dash&chat=https://relay.example/v1")
	if p.ID != "dash" {
		t.Errorf("id = %q", p.ID)
	}
}

func TestF07ImportLinkNameTruncation(t *testing.T) {
	long := strings.Repeat("界", 100)
	p := f07TestMust(t, "magpie://import?name="+long+"&id=x&chat=https://relay.example/v1")
	if got := len([]rune(p.Name)); got != 80 {
		t.Errorf("name runes = %d, want 80", got)
	}
}

func TestF07ImportLinkKey(t *testing.T) {
	f07TestRejects(t, "magpie://import?preset=deepseek&key=sk-abc%0A")
	f07TestRejects(t, "magpie://import?preset=deepseek&key=%0Ask-abc")
	f07TestRejects(t, "magpie://import?preset=deepseek&key=sk%09abc")
	f07TestRejects(t, "magpie://import?preset=deepseek&key="+strings.Repeat("k", 4097))
	p := f07TestMust(t, "magpie://import?preset=deepseek&key="+strings.Repeat("k", 4096))
	if len(p.Key) != 4096 {
		t.Errorf("key length = %d", len(p.Key))
	}
	if p := f07TestMust(t, "magpie://import?preset=deepseek"); p.Key != "" {
		t.Errorf("missing key = %q, want empty", p.Key)
	}
}

func TestF07ImportLinkRejectedBaseURLs(t *testing.T) {
	for _, chat := range []string{
		"http://relay.example/v1",
		"https://user:pw@relay.example/v1",
		"https://relay.example/v1?x=1",
		"https://relay.example/v1#frag",
		"file:///etc/passwd",
		"relay.example/v1",
		"ftp://relay.example/v1",
	} {
		f07TestRejects(t, "magpie://import?name=R&chat="+url.QueryEscape(chat))
	}
}

func TestF07ImportLinkLocalHTTPAllowed(t *testing.T) {
	for _, chat := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://[::1]:9000/v1",
		"http://box.local/v1",
	} {
		p := f07TestMust(t, "magpie://import?name=L&chat="+url.QueryEscape(chat))
		if p.Chat != strings.TrimRight(chat, "/") {
			t.Errorf("chat = %q, want %q", p.Chat, chat)
		}
	}
}

func TestF07ImportLinkModels(t *testing.T) {
	p := f07TestMust(t, "magpie://import?preset=deepseek&models=,%20a%20,,b,a,%20")
	if strings.Join(p.Models, "|") != "a|b" {
		t.Errorf("models = %q", p.Models)
	}
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, fmt.Sprintf("m%d", i))
	}
	f07TestMust(t, "magpie://import?preset=deepseek&models="+strings.Join(many, ","))
	many = append(many, "m200")
	f07TestRejects(t, "magpie://import?preset=deepseek&models="+strings.Join(many, ","))
}

func TestF07ImportLinkMissingPresetAndDuplicates(t *testing.T) {
	f07TestRejects(t, "magpie://import?preset=nope")
	f07TestRejects(t, "magpie://import?preset=deepseek&preset=openai")
}

func TestF07ImportLinkModelsWhitespaceAroundItems(t *testing.T) {
	cases := map[string]string{
		"magpie://import?preset=deepseek&models=a,%09b":                    "a|b",
		"magpie://import?preset=deepseek&models=%09a%09,b":                 "a|b",
		"magpie://import?preset=deepseek&models=a,%0Ab":                    "a|b",
		"magpie://import?preset=deepseek&models=%0A%20a%20%0A,%20%09,b%0D": "a|b",
	}
	for link, want := range cases {
		p := f07TestMust(t, link)
		if got := strings.Join(p.Models, "|"); got != want {
			t.Errorf("%s: models = %q, want %q", link, got, want)
		}
	}
}

func TestF07ImportLinkModelsControlInsideItemRejected(t *testing.T) {
	f07TestRejects(t, "magpie://import?preset=deepseek&models=a%01b")
	f07TestRejects(t, "magpie://import?preset=deepseek&models=a%00,b")
}
