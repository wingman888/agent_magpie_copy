package provider

import (
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc&models=a,%20b,,")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Key != "sk-abc" || p.Preset != "deepseek" || p.Chat != "https://api.deepseek.com/v1" {
		t.Fatalf("%+v", p)
	}
	if len(p.Models) != 2 || p.Models[0] != "a" || p.Models[1] != "b" {
		t.Fatalf("models %v", p.Models)
	}
	p, err = ParseImport("magpie://import?preset=deepseek&name=Mine&chat=http://evil.example/v1")
	if err != nil || p.Name != "Mine" || p.ID != "deepseek" || p.Chat != "https://api.deepseek.com/v1" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)&catalog=Bad%20One")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Chat != "https://relay.example/v1" || p.Anthropic != "https://relay.example" ||
		p.Website != "https://relay.example" || p.KeysURL != "" || p.Catalog != "" {
		t.Fatalf("%+v", p)
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://a.example/v1&catalog=openai&website=http://a.example")
	if err != nil || p.Catalog != "openai" || p.Website != "" {
		t.Fatalf("%+v %v", p, err)
	}
	long := strings.Repeat("é", 100)
	p, err = ParseImport("magpie://import?id=x&name=" + long + "&chat=https://a.example/v1")
	if err != nil || len([]rune(p.Name)) != 80 {
		t.Fatalf("%d %v", len([]rune(p.Name)), err)
	}
	var ms []string
	for i := 0; i < 300; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://a.example/v1&models=" + strings.Join(ms, ","))
	if err != nil || len(p.Models) != 200 {
		t.Fatalf("%d %v", len(p.Models), err)
	}
}

func TestParseImportRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=global")
	if err != nil || p.Chat != "https://global.yylx.io/v1" || p.Anthropic != "https://global.yylx.io" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Fatal("bad region accepted")
	}
}

func TestParseImportRejects(t *testing.T) {
	for _, l := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=X&chat=https://relay.example/v1?a=b",
		"magpie://import?name=X&chat=https://relay.example/v1%23f",
		"magpie://import?name=X&chat=https:///v1",
		"magpie://import?name=X&chat=ftp://relay.example/v1",
		"magpie://import?name=magpie&chat=https://a.example/v1",
		"magpie://import?name=X&id=magpie&chat=https://a.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://a.example/v1",
		"magpie://import?name=X&id=%E2%80%94&chat=https://a.example/v1",
		"magpie://import?name=X&chat=https://a.example/v1&key=sk-a%0Ab",
		"magpie://import?name=X&chat=https://a.example/v1&key=" + strings.Repeat("k", 4097),
		"magpie://import?preset=magpie",
	} {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("accepted %q", l)
		}
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, u := range []string{
		"http://localhost:11434/v1", "http://127.0.0.1:8080/v1", "http://192.168.1.20:8000/v1",
		"http://box.local/v1", "http://10.0.0.5/v1", "http://172.16.3.4/v1", "http://[::1]:8080/v1",
	} {
		p, err := ParseImport("magpie://import?name=L&chat=" + u)
		if err != nil || p.Chat != u {
			t.Errorf("%s: %+v %v", u, p, err)
		}
	}
	for _, u := range []string{"http://172.32.0.1/v1", "http://8.8.8.8/v1", "http://example.com/v1"} {
		if _, err := ParseImport("magpie://import?name=L&chat=" + u); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}

func TestParseImportForms(t *testing.T) {
	for _, l := range []string{
		"magpie://import?preset=deepseek", "magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek", "MAGPIE://import/?preset=deepseek",
	} {
		p, err := ParseImport(l)
		if err != nil || p.ID != "deepseek" {
			t.Errorf("%s: %v", l, err)
		}
	}
}
