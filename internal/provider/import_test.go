package provider

import (
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc" {
		t.Fatalf("got %+v", p)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Catalog != "deepseek" {
		t.Fatalf("preset endpoints not used: %+v", p)
	}
}

func TestParseImportPresetRegionAndName(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=cn&name=My%20Relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "My Relay" || p.ID != "yylx" {
		t.Fatalf("name override / id from preset: %+v", p)
	}
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Fatalf("region endpoints not used: %+v", p)
	}
	if p.Key != "" {
		t.Fatalf("key should be empty when omitted, got %q", p.Key)
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/&key=sk-1&models=a,%20b,,+c+,a&keys=https://acme.example/keys&website=http://acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "acme-relay" || p.Name != "Acme Relay" {
		t.Fatalf("id/name: %+v", p)
	}
	if p.Chat != "https://api.acme.example/v1" {
		t.Fatalf("trailing slash not removed: %q", p.Chat)
	}
	if strings.Join(p.Models, "|") != "a|b|c" {
		t.Fatalf("models: %v", p.Models)
	}
	if p.KeysURL != "https://acme.example/keys" {
		t.Fatalf("keys: %q", p.KeysURL)
	}
	if p.Website != "" {
		t.Fatalf("non-https website must be ignored, got %q", p.Website)
	}
}

func TestParseImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=X&chat=https://x.example/v1",
		"magpie:import?name=X&chat=https://x.example/v1",
		"magpie:///import?name=X&chat=https://x.example/v1",
		"MAGPIE://import?name=X&chat=https://x.example/v1",
		"Magpie:import/?name=X&chat=https://x.example/v1",
		"magpie://import/?name=X&chat=https://x.example/v1",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%s: %v", link, err)
			continue
		}
		if p.ID != "x" {
			t.Errorf("%s: id %q", link, p.ID)
		}
	}
}

func TestParseImportRejects(t *testing.T) {
	cases := map[string]string{
		"non magpie":        "https://example.com/import?name=X&chat=https://x.example/v1",
		"other scheme":      "magpiex://import?name=X&chat=https://x.example/v1",
		"delete action":     "magpie://delete?name=X&chat=https://x.example/v1",
		"import/extra":      "magpie://import/extra?name=X&chat=https://x.example/v1",
		"no name":           "magpie://import?chat=https://x.example/v1",
		"no base":           "magpie://import?name=X",
		"http public":       "magpie://import?name=X&chat=http://api.example.com/v1",
		"userinfo":          "magpie://import?name=X&chat=https://u:p@x.example/v1",
		"query in base":     "magpie://import?name=X&chat=https://x.example/v1?a=b",
		"fragment in base":  "magpie://import?name=X&chat=https://x.example/v1%23f",
		"file scheme":       "magpie://import?name=X&chat=file:///etc/passwd",
		"id magpie":         "magpie://import?name=X&id=magpie&chat=https://x.example/v1",
		"name no chars":     "magpie://import?name=%E2%98%85%E2%98%85&chat=https://x.example/v1",
		"key newline":       "magpie://import?name=X&chat=https://x.example/v1&key=sk%0Aevil",
		"unknown preset":    "magpie://import?preset=nope&key=k",
		"unknown region":    "magpie://import?preset=yylx&region=mars",
		"region no preset":  "magpie://import?name=X&chat=https://x.example/v1&region=cn",
		"explicit id slug":  "magpie://import?name=X&id=Bad%20Id&chat=https://x.example/v1",
		"too many models":   "magpie://import?name=X&chat=https://x.example/v1&models=" + strings.Repeat("m,", 201),
		"duplicate key":     "magpie://import?name=X&chat=https://x.example/v1&key=a&key=b",
		"bad escape":        "magpie://import?name=X&chat=https://x.example/v1&key=%zz",
		"empty id from sym": "magpie://import?name=***&chat=https://x.example/v1",
	}
	for what, link := range cases {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("%s: accepted %s", what, link)
		}
	}
}

func TestParseImportKeyLength(t *testing.T) {
	ok := "magpie://import?name=X&chat=https://x.example/v1&key=" + strings.Repeat("k", 4096)
	if _, err := ParseImport(ok); err != nil {
		t.Errorf("4096-char key rejected: %v", err)
	}
	long := "magpie://import?name=X&chat=https://x.example/v1&key=" + strings.Repeat("k", 4097)
	if _, err := ParseImport(long); err == nil {
		t.Error("4097-char key accepted")
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:8000/v1",
		"http://localhost:1234/v1",
		"http://192.168.1.20:8080/v1",
		"http://10.0.0.5/v1",
		"http://box.local/v1",
	} {
		if _, err := ParseImport("magpie://import?name=Local&chat=" + base); err != nil {
			t.Errorf("%s rejected: %v", base, err)
		}
	}
}

func TestParseImportIgnoresNonHTTPSLinks(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&website=ftp://x&keys=javascript:alert(1)")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "https://platform.deepseek.com" || p.KeysURL != "https://platform.deepseek.com/api_keys" {
		t.Fatalf("preset links were replaced by invalid ones: %+v", p)
	}
	q, err := ParseImport("magpie://import?name=X&chat=https://x.example/v1&website=ftp://x")
	if err != nil {
		t.Fatal(err)
	}
	if q.Website != "" {
		t.Fatalf("website kept: %q", q.Website)
	}
}

func TestParseImportDerivedID(t *testing.T) {
	cases := map[string]string{
		"Acme Relay": "acme-relay",
		"My Relay":   "my-relay",
		"--Foo!!":    "foo",
	}
	for name, want := range cases {
		p, err := ParseImport("magpie://import?chat=https://x.example/v1&name=" + strings.ReplaceAll(name, " ", "%20"))
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if p.ID != want {
			t.Errorf("%q: id %q, want %q", name, p.ID, want)
		}
	}
}

func TestParseImportNameTruncated(t *testing.T) {
	long := strings.Repeat("a", 100)
	p, err := ParseImport("magpie://import?name=" + long + "&chat=https://x.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(p.Name)) != 80 {
		t.Fatalf("name length %d", len([]rune(p.Name)))
	}
}

func TestParseImportDoesNotWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := ParseImport("magpie://import?name=X&chat=https://x.example/v1&key=k"); err != nil {
		t.Fatal(err)
	}
	if got := load().Providers; len(got) != 0 {
		t.Fatalf("ParseImport saved %d providers", len(got))
	}
}

func TestParseImportErrorsDoNotEchoKey(t *testing.T) {
	_, err := ParseImport("magpie://import?name=X&chat=https://x.example/v1&key=sk-SECRET%0A")
	if err == nil {
		t.Fatal("control character accepted")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error echoes key: %v", err)
	}
	_, err = ParseImport("magpie://import?name=X&chat=https://x.example/v1&key=sk-SECRET%zz")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("bad escape error: %v", err)
	}
}
