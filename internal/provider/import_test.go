package provider

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Name != "DeepSeek" || p.Preset != "deepseek" {
		t.Errorf("id/name/preset = %q/%q/%q", p.ID, p.Name, p.Preset)
	}
	if p.Key != "sk-abc" {
		t.Errorf("key = %q", p.Key)
	}
	if strings.Join(p.Models, ",") != "a,b" {
		t.Errorf("models = %v", p.Models)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("endpoints = %q / %q", p.Chat, p.Anthropic)
	}
}

func TestParseImportPresetNameIdAndCatalogOverrides(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&name=DS%20Mine&id=ds-mine&catalog=openai&website=https://evil.example&chat=https://other.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "DS Mine" || p.ID != "ds-mine" {
		t.Errorf("name/id = %q/%q", p.Name, p.ID)
	}
	// catalog, website and endpoints come from the preset, not the link
	if p.Catalog != "deepseek" || p.Website != "https://platform.deepseek.com" || p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("preset fields overridden: catalog=%q website=%q chat=%q", p.Catalog, p.Website, p.Chat)
	}
	if p.Key != "" {
		t.Errorf("no key given, got %q", p.Key)
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)&key=sk-x")
	if err != nil {
		t.Fatal(err)
	}
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
		t.Errorf("keys = %q, want javascript: ignored", p.KeysURL)
	}
	if p.Key != "sk-x" {
		t.Errorf("key = %q", p.Key)
	}
}

func TestParseImportCustomFields(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme&id=Acme%20Relay&responses=https://api.acme.example/v1&models=gpt-5.5,%20,claude-sonnet-5,gpt-5.5&catalog=Bad%20Value")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "acme-relay" {
		t.Errorf("explicit id not normalized: %q", p.ID)
	}
	if p.Responses != "https://api.acme.example/v1" {
		t.Errorf("responses = %q", p.Responses)
	}
	if strings.Join(p.Models, ",") != "gpt-5.5,claude-sonnet-5" {
		t.Errorf("models = %v", p.Models)
	}
	if p.Catalog != "" {
		t.Errorf("invalid catalog kept: %q", p.Catalog)
	}
	p, err = ParseImport("magpie://import?name=Acme&chat=https://a.example/v1&catalog=openai")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai" {
		t.Errorf("catalog = %q", p.Catalog)
	}
}

func TestParseImportRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=global&key=k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://global.yylx.io/v1" || p.Anthropic != "https://global.yylx.io" {
		t.Errorf("region endpoints = %q / %q", p.Chat, p.Anthropic)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Error("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Error("region on a preset without regions accepted")
	}
	if _, err := ParseImport("magpie://import?name=R&region=cn&chat=https://r.example/v1"); err == nil {
		t.Error("region without preset accepted")
	}
}

func TestParseImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  magpie://import?preset=deepseek  ",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%q: %v", link, err)
			continue
		}
		if p.ID != "deepseek" {
			t.Errorf("%q: id = %q", link, p.ID)
		}
	}
}

func TestParseImportRejects(t *testing.T) {
	bad := []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import/extra?preset=deepseek",
		"magpie://import?preset=nope",
		"magpie://import?name=Relay",
		"magpie://import?name=Relay&chat=http://relay.example/v1",
		"magpie://import?name=Relay&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=Relay&chat=file:///etc/passwd",
		"magpie://import?name=Relay&chat=https://relay.example/v1?x=1",
		"magpie://import?name=Relay&chat=https://relay.example/v1%23frag",
		"magpie://import?name=Relay&chat=relay.example/v1",
		"magpie://import?name=magpie&chat=https://relay.example/v1",
		"magpie://import?name=Relay&id=MAGPIE&chat=https://relay.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://relay.example/v1",
		"magpie://import?preset=deepseek&key=sk-abc%0A",
		"magpie://import?preset=deepseek&key=sk-abc%0Dx",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("k", importMaxKey+1),
		"magpie://import?name=Relay&chat=https://relay.example/v1&key=%0Asecret",
		"magpie://import?name=Relay&chat=https://relay.example/v1&key=" + strings.Repeat("k", importMaxKey+1),
		"magpie://import?name=Relay&chat=https://relay.example/v1#frag",
		"magpie://import?name=Relay&chat=https://relay.example/v1&models=%0A",
		"magpie://import?name=Relay&chat=https://relay.example/v1&id=%E2%80%94",
		"magpie://import?name=%0ARelay&chat=https://relay.example/v1",
		"magpie://import?name=Relay&chat=http://%0Arelay.example/v1",
		"magpie://import?name=Relay&chat=https://relay.example:%20/v1",
		"magpie://import?name=Relay&chat=https://",
		"magpie://import?name=Relay&chat=https://relay.example/v1&anthropic=http://relay.example",
		"magpie://import?preset=%zz",
	}
	for _, link := range bad {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("accepted %q", link)
		}
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, chat := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.16.4.4/v1",
		"http://[::1]:9000/v1",
		"http://box.local/v1",
	} {
		link := "magpie://import?name=Local&chat=" + url.QueryEscape(chat)
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%s: %v", chat, err)
			continue
		}
		if p.Chat != chat {
			t.Errorf("chat = %q, want %q", p.Chat, chat)
		}
	}
	// private-looking but public-facing names stay refused
	for _, chat := range []string{"http://relay.example/v1", "http://8.8.8.8/v1", "http://local.example/v1"} {
		if _, err := ParseImport("magpie://import?name=L&chat=" + url.QueryEscape(chat)); err == nil {
			t.Errorf("accepted %s", chat)
		}
	}
}

func TestParseImportNameAndModelLimits(t *testing.T) {
	long := strings.Repeat("a", 100)
	p, err := ParseImport("magpie://import?name=" + long + "&chat=https://relay.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Name)); n != importMaxName {
		t.Errorf("name length = %d, want %d", n, importMaxName)
	}

	ms := make([]string, 250)
	for i := range ms {
		ms[i] = "m" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + "-" + strings.Repeat("1", i/26)
	}
	p, err = ParseImport("magpie://import?name=R&chat=https://relay.example/v1&models=" + url.QueryEscape(strings.Join(ms, ",")))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != importMaxModels {
		t.Errorf("models = %d, want %d", len(p.Models), importMaxModels)
	}
}

func TestParseImportWithoutKeyOnLocalPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=ollama")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "" || !keyOptional(p) {
		t.Errorf("key = %q, keyOptional = %v", p.Key, keyOptional(p))
	}
}
