package provider

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseImportPresetL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-test" {
		t.Errorf("got id=%q preset=%q key=%q", p.ID, p.Preset, p.Key)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("bases from preset: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	if p.Catalog != "deepseek" || p.Website == "" || p.KeysURL == "" {
		t.Errorf("catalog/website from preset: %q %q %q", p.Catalog, p.Website, p.KeysURL)
	}
}

func TestParseImportPresetRegionL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=cn&key=k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	// no region: the first region is the default
	d, err := ParseImport("magpie://import?preset=yylx")
	if err != nil {
		t.Fatal(err)
	}
	if d.Chat != "https://app.yylx.io/v1" {
		t.Errorf("default region: chat=%q", d.Chat)
	}
	if d.Key != "" {
		t.Errorf("no key given but key=%q", d.Key)
	}
}

func TestParseImportPresetNameAndIDL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&name=My%20DS&id=my-ds")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "My DS" || p.ID != "my-ds" || p.Preset != "deepseek" {
		t.Errorf("got name=%q id=%q preset=%q", p.Name, p.ID, p.Preset)
	}
}

func TestParseImportCustomL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/&key=sk-x&models=a,%20b,,%20,c,a&catalog=openai")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Acme Relay" || p.ID != "acme-relay" {
		t.Errorf("got name=%q id=%q", p.Name, p.ID)
	}
	if p.Chat != "https://api.acme.example/v1" {
		t.Errorf("chat=%q (trailing slash should go)", p.Chat)
	}
	if p.Key != "sk-x" || p.Preset != "" || p.Catalog != "openai" {
		t.Errorf("key=%q preset=%q catalog=%q", p.Key, p.Preset, p.Catalog)
	}
	if strings.Join(p.Models, ",") != "a,b,c" {
		t.Errorf("models=%v", p.Models)
	}
}

func TestParseImportEquivalentSchemesL3x(t *testing.T) {
	links := []string{
		"magpie://import?name=X&chat=https://a.example/v1",
		"magpie:import?name=X&chat=https://a.example/v1",
		"magpie:///import?name=X&chat=https://a.example/v1",
		"MAGPIE://import?name=X&chat=https://a.example/v1",
		"Magpie:Import?name=X&chat=https://a.example/v1",
		"magpie://import/?name=X&chat=https://a.example/v1",
	}
	for _, l := range links {
		p, err := ParseImport(l)
		if err != nil {
			t.Errorf("%s: %v", l, err)
			continue
		}
		if p.ID != "x" {
			t.Errorf("%s: id %q", l, p.ID)
		}
	}
}

func TestParseImportRejectsL3x(t *testing.T) {
	cases := map[string]string{
		"not magpie":          "https://import?name=A&chat=https://x.example/v1",
		"file scheme":         "file://import?name=A&chat=https://x.example/v1",
		"other action":        "magpie://delete?name=A&chat=https://x.example/v1",
		"no action":           "magpie://?name=A&chat=https://x.example/v1",
		"missing name":        "magpie://import?chat=https://x.example/v1",
		"missing base":        "magpie://import?name=A",
		"http public":         "magpie://import?name=A&chat=http://api.example.com/v1",
		"credentials":         "magpie://import?name=A&chat=https://u:p@api.example.com/v1",
		"file base":           "magpie://import?name=A&chat=file:///etc/passwd",
		"query in base":       "magpie://import?name=A&chat=https://x.example/v1?k=1",
		"fragment in base":    "magpie://import?name=A&chat=https://x.example/v1%23frag",
		"id magpie":           "magpie://import?name=A&id=magpie&chat=https://x.example/v1",
		"name magpie":         "magpie://import?name=Magpie&chat=https://x.example/v1",
		"name no usable":      "magpie://import?name=---&chat=https://x.example/v1",
		"name only cjk":       "magpie://import?name=%E9%B1%BC%E9%B1%BC&chat=https://x.example/v1",
		"id not slug":         "magpie://import?name=A&id=My_ID&chat=https://x.example/v1",
		"key newline":         "magpie://import?name=A&chat=https://x.example/v1&key=sk-abc%0Adef",
		"key too long":        "magpie://import?name=A&chat=https://x.example/v1&key=" + strings.Repeat("a", 4097),
		"unknown preset":      "magpie://import?preset=nosuch&key=k",
		"unknown region":      "magpie://import?preset=yylx&region=mars&key=k",
		"region without pres": "magpie://import?name=A&region=cn&chat=https://x.example/v1",
		"region on plain":     "magpie://import?preset=deepseek&region=cn",
		"too many models":     "magpie://import?name=A&chat=https://x.example/v1&models=" + manyNamesL3x(201),
		"duplicate param":     "magpie://import?name=A&name=B&chat=https://x.example/v1",
		"bad base scheme":     "magpie://import?name=A&chat=ftp://x.example/v1",
	}
	for what, l := range cases {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("%s: accepted %s", what, l)
		}
	}
}

func TestParseImportErrorsHideKeyL3x(t *testing.T) {
	const secret = "sk-supersecret123"
	bad := []string{
		"magpie://import?name=A&chat=https://x.example/v1&key=" + secret + "%0A",
		"magpie://import?name=A&chat=http://public.example/v1&key=" + secret,
		"magpie://import?name=A&chat=https://x.example/v1&key=" + secret + "&key=" + secret,
		"magpie://import?name=A&chat=ht%zztp://x.example&key=" + secret,
		"magpie://import?name=A&chat=https://x.example/v1&key=%zz" + secret,
	}
	for _, l := range bad {
		_, err := ParseImport(l)
		if err == nil {
			t.Errorf("accepted %q", l)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks key: %v", err)
		}
	}
}

func TestParseImportLocalHTTPAcceptedL3x(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:8080/v1",
		"http://localhost:1234/v1",
		"http://192.168.1.10/v1",
		"http://10.0.0.5/v1",
		"http://box.local/v1",
		"http://[::1]:11434/v1",
	} {
		p, err := ParseImport("magpie://import?name=Local&chat=" + base)
		if err != nil {
			t.Errorf("%s: %v", base, err)
			continue
		}
		if p.Chat != base {
			t.Errorf("chat=%q want %q", p.Chat, base)
		}
	}
}

func TestParseImportNonHTTPSLinksIgnoredL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?name=A&chat=https://x.example/v1&website=http://x.example&keys=file:///tmp/k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "" || p.KeysURL != "" {
		t.Errorf("non-https website/keys kept: %q %q", p.Website, p.KeysURL)
	}
	q, err := ParseImport("magpie://import?name=A&chat=https://x.example/v1&website=https://x.example&keys=https://x.example/keys")
	if err != nil {
		t.Fatal(err)
	}
	if q.Website != "https://x.example" || q.KeysURL != "https://x.example/keys" {
		t.Errorf("https website/keys: %q %q", q.Website, q.KeysURL)
	}
}

func TestParseImportTruncatesNameL3x(t *testing.T) {
	p, err := ParseImport("magpie://import?name=" + strings.Repeat("a", 100) + "&chat=https://x.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Name) != 80 {
		t.Errorf("name length %d, want 80", len(p.Name))
	}
}

func manyNamesL3x(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = "m" + strconv.Itoa(i)
	}
	return strings.Join(names, ",")
}
