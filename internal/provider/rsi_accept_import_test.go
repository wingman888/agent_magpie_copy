package provider_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptImportPreset(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek&key=sk-abc123&models=deepseek-v4-pro,%20deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc123" || p.Chat == "" {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Models, []string{"deepseek-v4-pro", "deepseek-v4-flash"}) {
		t.Fatalf("models %q", p.Models)
	}
	p2, err := provider.ParseImport("magpie://import?preset=DeepSeek")
	if err != nil || p2.Preset != "deepseek" {
		t.Fatalf("case-insensitive preset: %+v %v", p2, err)
	}
}

func TestAcceptImportCustom(t *testing.T) {
	q := url.Values{
		"name":      {"My Relay"},
		"chat":      {"https://relay.example/v1/"},
		"anthropic": {"https://relay.example"},
		"key":       {"sk-relay"},
		"website":   {"https://relay.example"},
		"keys":      {"javascript:alert(1)"},
		"catalog":   {"openai"},
	}
	p, err := provider.ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" || p.Chat != "https://relay.example/v1" || p.Anthropic != "https://relay.example" || p.Preset != "" || p.Key != "sk-relay" {
		t.Fatalf("got %+v", p)
	}
	if p.Website != "https://relay.example" || p.KeysURL != "" {
		t.Fatalf("pages: %q %q", p.Website, p.KeysURL)
	}
	if p.Catalog != "openai" || p.Icon != provider.IconForCatalog("openai") {
		t.Fatalf("catalog/icon: %q %q", p.Catalog, p.Icon)
	}
}

func TestAcceptImportCatalogAndPages(t *testing.T) {
	base := "magpie://import?name=X&chat=https://x.example/v1"
	p, err := provider.ParseImport(base + "&catalog=Open%20AI&website=http://x.example&keys=https://user:pw@x.example/k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "" || p.Website != "" || p.KeysURL != "" {
		t.Fatalf("invalid values kept: %+v", p)
	}
	p, err = provider.ParseImport(base + "&id=Custom%20ID&catalog=OpenAI")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != provider.Slug("Custom ID") || p.Catalog != "openai" {
		t.Fatalf("got %+v", p)
	}
}

func TestAcceptImportRegion(t *testing.T) {
	var pr provider.PresetDef
	for _, d := range provider.Presets() {
		if len(d.Regions) > 1 {
			pr = d
			break
		}
	}
	if pr.ID == "" {
		t.Skip("no preset with regions")
	}
	r := pr.Regions[1]
	p, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=" + r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != r.Chat || p.Anthropic != r.Anthropic || p.Preset != pr.ID {
		t.Fatalf("got %+v, want region %+v", p, r)
	}
	if _, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestAcceptImportRejects(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=deepseek",
		"magpie://import",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=https://relay.example/v1?a=b",
		"magpie://import?name=X&chat=https://relay.example/v1%23frag",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://x.example/v1",
		"magpie://import?preset=deepseek&key=sk%0Aevil",
		"magpie://import?preset=deepseek&key=sk%7Fevil",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("a", 4097),
	} {
		if p, err := provider.ParseImport(link); err == nil {
			t.Errorf("%s: accepted as %+v", link, p)
		} else if p.ID != "" || p.Chat != "" {
			t.Errorf("%s: non-zero provider on error: %+v", link, p)
		}
	}
	if _, err := provider.ParseImport("magpie://import?preset=deepseek&key=" + strings.Repeat("a", 4096)); err != nil {
		t.Errorf("4096-byte key rejected: %v", err)
	}
}

func TestAcceptImportLocalHTTP(t *testing.T) {
	for _, host := range []string{"localhost:11434", "127.0.0.1:8080", "192.168.1.20:8000", "box.local"} {
		p, err := provider.ParseImport("magpie://import?name=Local&chat=http://" + host + "/v1")
		if err != nil {
			t.Errorf("%s: %v", host, err)
		} else if p.Chat != "http://"+host+"/v1" {
			t.Errorf("%s: chat %q", host, p.Chat)
		}
	}
}

func TestAcceptImportForms(t *testing.T) {
	for _, link := range []string{
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  magpie://import?preset=deepseek \n",
	} {
		p, err := provider.ParseImport(link)
		if err != nil || p.ID != "deepseek" {
			t.Errorf("%q: %+v %v", link, p, err)
		}
	}
}

func TestAcceptImportLimits(t *testing.T) {
	long := strings.Repeat("n", 120)
	p, err := provider.ParseImport("magpie://import?id=r&name=" + long + "&chat=https://x.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Name) != 80 {
		t.Fatalf("name len %d", len(p.Name))
	}
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("0", i/26))
	}
	p, err = provider.ParseImport("magpie://import?preset=deepseek&models=" + url.QueryEscape(strings.Join(ms, ",")))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 200 || p.Models[0] != ms[0] || p.Models[199] != ms[199] {
		t.Fatalf("models len %d", len(p.Models))
	}
}

func TestAcceptImportPresetNameOverride(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek&name=My%20DS")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "My DS" || p.Preset != "deepseek" {
		t.Fatalf("got %+v", p)
	}
}
