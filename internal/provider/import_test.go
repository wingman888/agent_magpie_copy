package provider

import (
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=DeepSeek&key=sk-abc123&models=deepseek-v4-pro,%20deepseek-v4-flash,,")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc123" || p.Chat == "" {
		t.Fatalf("%+v", p)
	}
	if len(p.Models) != 2 || p.Models[0] != "deepseek-v4-pro" || p.Models[1] != "deepseek-v4-flash" {
		t.Fatalf("models %q", p.Models)
	}
	p, err = ParseImport("magpie://import?preset=deepseek&name=Mine&website=http://x&chat=https://evil.example/v1")
	if err != nil || p.Name != "Mine" || p.Preset != "deepseek" || strings.Contains(p.Chat, "evil") {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https%3A%2F%2Frelay.example%2Fv1%2F&anthropic=https://relay.example&key=k&website=https://relay.example&keys=javascript:alert(1)&catalog=openai")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" || p.Chat != "https://relay.example/v1" || p.Anthropic != "https://relay.example" ||
		p.Preset != "" || p.Website != "https://relay.example" || p.KeysURL != "" || p.Catalog != "openai" || p.Icon != IconForCatalog("openai") {
		t.Fatalf("%+v", p)
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://x.example/v1&catalog=Open%20AI&id=Foo%20Bar")
	if err != nil || p.Catalog != "" || p.ID != "foo-bar" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestParseImportRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=cn")
	if err != nil || p.Preset != "yylx" || p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Fatal("bad region accepted")
	}
}

func TestParseImportErrors(t *testing.T) {
	for _, l := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=deepseek",
		"magpie://import",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=X&chat=https://x.example/v1?a=b",
		"magpie://import?name=X&chat=https://x.example/v1%23f",
		"magpie://import?name=X&chat=https:///v1",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://x.example/v1",
		"magpie://import?preset=deepseek&key=sk%0Aevil",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("a", 4097),
	} {
		if p, err := ParseImport(l); err == nil {
			t.Errorf("%s accepted", l)
		} else if p.ID != "" || p.Name != "" || p.Key != "" {
			t.Errorf("%s: non-zero provider", l)
		}
	}
	_, err := ParseImport("https://usemagpie.ai/import")
	if err == nil || err.Error() != "not a magpie:// link" {
		t.Fatal(err)
	}
	_, err = ParseImport("magpie://delete?id=x")
	if err == nil || err.Error() != "magpie://delete is not something magpie knows; links start magpie://import?" {
		t.Fatal(err)
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, h := range []string{"localhost:11434", "127.0.0.1:8080", "192.168.1.20:8000", "box.local"} {
		p, err := ParseImport("magpie://import?name=L&chat=http://" + h + "/v1")
		if err != nil || p.Chat != "http://"+h+"/v1" {
			t.Errorf("%s: %+v %v", h, p, err)
		}
	}
}

func TestParseImportForms(t *testing.T) {
	for _, l := range []string{"magpie:import?preset=deepseek", "magpie:///import?preset=deepseek", "MAGPIE://import/?preset=deepseek", "  magpie://import?preset=deepseek \n"} {
		if p, err := ParseImport(l); err != nil || p.ID != "deepseek" {
			t.Errorf("%q: %v", l, err)
		}
	}
}

func TestParseImportLimits(t *testing.T) {
	p, err := ParseImport("magpie://import?chat=https://x.example/v1&name=" + strings.Repeat("a", 100))
	if err != nil || len(p.Name) != 80 {
		t.Fatalf("%d %v", len(p.Name), err)
	}
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("1", i/26))
	}
	p, err = ParseImport("magpie://import?preset=deepseek&models=" + strings.Join(ms, ","))
	if err != nil || len(p.Models) != 200 {
		t.Fatalf("%d %v", len(p.Models), err)
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&key=" + strings.Repeat("a", 4096)); err != nil {
		t.Fatal(err)
	}
}
