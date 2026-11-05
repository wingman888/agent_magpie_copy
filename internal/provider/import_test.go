package provider

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-test-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Name != "DeepSeek" || p.Preset != "deepseek" || p.Key != "sk-test-1234567890" {
		t.Fatalf("preset: %+v", p)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Fatalf("preset endpoints: %+v", p)
	}
	if p.Catalog == "" || p.Website == "" || p.KeysURL == "" {
		t.Fatalf("preset metadata missing: %+v", p)
	}

	// name overrides the preset's name; id stays the preset's
	p, err = ParseImport("magpie://import?preset=deepseek&name=Mine")
	if err != nil || p.Name != "Mine" || p.ID != "deepseek" || p.Key != "" {
		t.Fatalf("override: %+v %v", p, err)
	}
}

func TestParseImportRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=global&key=k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://global.yylx.io/v1" || p.Anthropic != "https://global.yylx.io" {
		t.Fatalf("region endpoints: %+v", p)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=mars"); err == nil {
		t.Fatal("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Fatal("region on a preset without regions accepted")
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-abc")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "acme-relay" || p.Name != "Acme Relay" || p.Chat != "https://api.acme.example/v1" || p.Key != "sk-abc" {
		t.Fatalf("custom: %+v", p)
	}
	if p.Preset != "" || p.Responses != "" || p.Anthropic != "" {
		t.Fatalf("custom should carry only what the link says: %+v", p)
	}

	p, err = ParseImport("magpie://import?name=My%20Relay&anthropic=https://gw.example.com/&responses=https://r.example.com/v1/")
	if err != nil || p.ID != "my-relay" || p.Anthropic != "https://gw.example.com" || p.Responses != "https://r.example.com/v1" {
		t.Fatalf("trailing slash: %+v %v", p, err)
	}

	// explicit id must already be a slug
	if p, err := ParseImport("magpie://import?name=X&id=my-id&chat=https://a.example/v1"); err != nil || p.ID != "my-id" {
		t.Fatalf("explicit id: %+v %v", p, err)
	}
	if _, err := ParseImport("magpie://import?name=X&id=My_Id&chat=https://a.example/v1"); err == nil {
		t.Fatal("non-slug id accepted")
	}
	if _, err := ParseImport("magpie://import?name=X&id=magpie&chat=https://a.example/v1"); err == nil {
		t.Fatal(`id "magpie" accepted`)
	}
	if _, err := ParseImport("magpie://import?name=Magpie&chat=https://a.example/v1"); err == nil {
		t.Fatal(`derived id "magpie" accepted`)
	}
}

func TestParseImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import?preset=deepseek",
		"Magpie:Import?preset=deepseek",
		"magpie://import/?preset=deepseek",
		"magpie:///import/?preset=deepseek",
	} {
		if p, err := ParseImport(link); err != nil || p.ID != "deepseek" {
			t.Errorf("%s: %+v %v", link, p, err)
		}
	}
	for _, link := range []string{
		"magpie://delete?preset=deepseek",
		"magpie://?preset=deepseek",
		"https://import?preset=deepseek",
		"file://import?preset=deepseek",
		"deepseek?preset=deepseek",
		"",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("accepted %q", link)
		}
	}
}

func TestParseImportRejects(t *testing.T) {
	for name, link := range map[string]string{
		"no name and no preset":        "magpie://import?chat=https://a.example/v1",
		"no base URL":                  "magpie://import?name=X",
		"public http":                  "magpie://import?name=X&chat=http://api.example.com/v1",
		"userinfo":                     "magpie://import?name=X&chat=https://user:pw@api.example.com/v1",
		"file scheme":                  "magpie://import?name=X&chat=file:///etc/passwd",
		"query in base":                "magpie://import?name=X&chat=https://a.example/v1?x=1",
		"fragment in base":             "magpie://import?name=X&chat=https://a.example/v1%23frag",
		"no scheme":                    "magpie://import?name=X&chat=a.example/v1",
		"symbols-only name":            "magpie://import?name=%21%21%21&chat=https://a.example/v1",
		"key with newline":             "magpie://import?name=X&chat=https://a.example/v1&key=sk-1%0A",
		"key with control char":        "magpie://import?name=X&chat=https://a.example/v1&key=sk%01",
		"key too long":                 "magpie://import?name=X&chat=https://a.example/v1&key=" + strings.Repeat("k", 4097),
		"unknown preset":               "magpie://import?preset=nosuch&key=k",
		"duplicate parameter":          "magpie://import?name=X&name=Y&chat=https://a.example/v1",
		"too many models":              "magpie://import?name=X&chat=https://a.example/v1&models=" + modelList(201),
		"id not a slug":                "magpie://import?name=X&id=Acme&chat=https://a.example/v1",
		"anthropic public http":        "magpie://import?name=X&anthropic=http://a.example",
		"region without preset":        "magpie://import?name=X&region=cn&chat=https://a.example/v1",
	} {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%s: accepted: %+v", name, p)
		}
	}
}

func TestParseImportLocalHTTPAccepted(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:11434/v1",
		"http://localhost:1234/v1",
		"http://192.168.1.20:8080/v1",
		"http://10.0.0.5/v1",
		"http://box.local:9000/v1",
		"http://[::1]:8080/v1",
	} {
		if p, err := ParseImport("magpie://import?name=Local&chat=" + url_(base)); err != nil || p.Chat != base {
			t.Errorf("%s: %+v %v", base, p, err)
		}
	}
}

func TestParseImportWebsiteAndKeysNeedHTTPS(t *testing.T) {
	p, err := ParseImport("magpie://import?name=X&chat=https://a.example/v1&website=http://a.example&keys=ftp://a.example/keys")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "" || p.KeysURL != "" {
		t.Fatalf("non-https website/keys kept: %+v", p)
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://a.example/v1&website=https://a.example/&keys=https://a.example/keys")
	if err != nil || p.Website != "https://a.example" || p.KeysURL != "https://a.example/keys" {
		t.Fatalf("https website/keys: %+v %v", p, err)
	}
}

func TestParseImportModelsAndName(t *testing.T) {
	p, err := ParseImport("magpie://import?name=X&chat=https://a.example/v1&models=" + url_(" a-1 , ,b-2,, a-1 "))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Models, "|") != "a-1|b-2" {
		t.Fatalf("models: %q", p.Models)
	}
	if _, err := ParseImport("magpie://import?name=X&chat=https://a.example/v1&models=" + modelList(200)); err != nil {
		t.Fatalf("200 models rejected: %v", err)
	}

	long := strings.Repeat("n", 90)
	p, err = ParseImport("magpie://import?name=" + long + "&chat=https://a.example/v1")
	if err != nil || len([]rune(p.Name)) != 80 {
		t.Fatalf("long name: %q %v", p.Name, err)
	}
}

func TestParseImportCatalogAndKeyWhitespace(t *testing.T) {
	p, err := ParseImport("magpie://import?name=X&chat=https://a.example/v1&catalog=openai&key=%20sk-x%20")
	if err != nil || p.Catalog != "openai" || p.Key != "sk-x" {
		t.Fatalf("catalog/key: %+v %v", p, err)
	}
}

func TestSlugMatchesImportDerivation(t *testing.T) {
	for in, want := range map[string]string{"Acme Relay": "acme-relay", "My Relay": "my-relay", "  --Hi!!--  ": "hi"} {
		p, err := ParseImport("magpie://import?name=" + url_(in) + "&chat=https://a.example/v1")
		if err != nil || p.ID != want || p.ID != Slug(in) {
			t.Errorf("%q: %+v %v (want %q)", in, p, err, want)
		}
	}
}

// url_ escapes the characters a test value may contain.
func url_(s string) string {
	r := strings.NewReplacer(" ", "%20", "+", "%2B", "[", "%5B", "]", "%5D", "!", "%21", "%0A", "%0A")
	return r.Replace(s)
}

func modelList(n int) string {
	var xs []string
	for i := 0; i < n; i++ {
		xs = append(xs, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strconv.Itoa(i))
	}
	return strings.Join(xs, ",")
}
