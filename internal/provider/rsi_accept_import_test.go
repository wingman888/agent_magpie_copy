package provider_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptPreset(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek&key=sk-abc&models=a,%20b,,")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc" || p.Chat == "" {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Models, []string{"a", "b"}) {
		t.Fatalf("models %q", p.Models)
	}
	if provider.Scheme != "magpie" {
		t.Fatal("scheme")
	}
}

func TestAcceptCustom(t *testing.T) {
	q := url.Values{
		"name": {"My Relay"}, "chat": {"https://relay.example/v1/"}, "anthropic": {"https://relay.example"},
		"key": {"sk-relay"}, "website": {"https://relay.example"}, "keys": {"javascript:alert(1)"},
	}
	p, err := provider.ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" || p.Chat != "https://relay.example/v1" || p.Anthropic != "https://relay.example" || p.Preset != "" {
		t.Fatalf("got %+v", p)
	}
	if p.Website != "https://relay.example" || p.KeysURL != "" {
		t.Fatalf("pages %q %q", p.Website, p.KeysURL)
	}
}

func TestAcceptRegion(t *testing.T) {
	var pr *provider.PresetDef
	for _, d := range provider.Presets() {
		if len(d.Regions) > 1 {
			d := d
			pr = &d
			break
		}
	}
	if pr == nil {
		t.Skip("no regions")
	}
	r := pr.Regions[1]
	p, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=" + r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != r.Chat || p.Anthropic != r.Anthropic {
		t.Fatalf("got %+v want %+v", p, r)
	}
	if _, err := provider.ParseImport("magpie://import?preset=" + pr.ID + "&region=nowhere"); err == nil {
		t.Fatal("bad region accepted")
	}
}

func TestAcceptRejects(t *testing.T) {
	long := strings.Repeat("k", 4097)
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=X&chat=https://x.example/v1?a=b",
		"magpie://import?name=X&chat=https://x.example/v1%23frag",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=X&id=magpie&chat=https://x.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://x.example/v1",
		"magpie://import?preset=deepseek&key=sk%0Aevil",
		"magpie://import?preset=deepseek&key=" + long,
	} {
		if p, err := provider.ParseImport(link); err == nil {
			t.Errorf("%s accepted as %+v", link, p)
		}
	}
}

func TestAcceptLocalHTTP(t *testing.T) {
	for _, h := range []string{"localhost:11434", "127.0.0.1:8080", "192.168.1.20:8000", "box.local"} {
		if _, err := provider.ParseImport("magpie://import?name=Local&chat=http://" + h + "/v1"); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
}

func TestAcceptForms(t *testing.T) {
	for _, l := range []string{"magpie:import?preset=deepseek", "magpie:///import?preset=deepseek", "MAGPIE://import/?preset=deepseek"} {
		p, err := provider.ParseImport(l)
		if err != nil || p.ID != "deepseek" {
			t.Errorf("%s: %v %+v", l, err, p)
		}
	}
}

func TestAcceptLimits(t *testing.T) {
	q := url.Values{"name": {strings.Repeat("n", 120)}, "chat": {"https://x.example/v1"}, "catalog": {"Bad Value!"}}
	var ms []string
	for i := 0; i < 300; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+url.QueryEscape(strings.Repeat("0", i/26+1)))
	}
	q.Set("models", strings.Join(ms, ","))
	p, err := provider.ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Name) > 80 {
		t.Errorf("name len %d", len(p.Name))
	}
	if len(p.Models) != 200 {
		t.Errorf("models %d", len(p.Models))
	}
	if p.Catalog != "" {
		t.Errorf("catalog %q", p.Catalog)
	}
}

func TestAcceptExplicitIDAndNamePreset(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek&name=Mine&id=my-ds")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-ds" || p.Name != "Mine" || p.Preset != "deepseek" {
		t.Fatalf("%+v", p)
	}
}
