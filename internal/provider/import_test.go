package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseImportSchemes(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=Acme&chat=https://a.example/v1",
		"magpie:import?name=Acme&chat=https://a.example/v1",
		"magpie:///import?name=Acme&chat=https://a.example/v1",
		"MAGPIE://import?name=Acme&chat=https://a.example/v1",
		"Magpie:Import?name=Acme&chat=https://a.example/v1",
		"magpie://import/?name=Acme&chat=https://a.example/v1",
		"magpie:///import/?name=Acme&chat=https://a.example/v1",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%q: unexpected error %v", link, err)
			continue
		}
		if p.ID != "acme" || p.Name != "Acme" || p.Chat != "https://a.example/v1" {
			t.Errorf("%q: got %+v", link, p)
		}
	}
}

func TestParseImportRejectsOtherSchemesAndActions(t *testing.T) {
	for _, link := range []string{
		"https://import?name=Acme&chat=https://a.example/v1",
		"http://import?name=Acme&chat=https://a.example/v1",
		"file:///import?name=Acme&chat=https://a.example/v1",
		"magpie://delete?name=Acme&chat=https://a.example/v1",
		"magpie:///delete?name=Acme&chat=https://a.example/v1",
		"magpie://import/extra?name=Acme&chat=https://a.example/v1",
		"magpie://?name=Acme&chat=https://a.example/v1",
		"deepseek://import?preset=deepseek",
		"",
		"not a link",
	} {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%q: accepted as %+v, want rejection", link, p)
		}
	}
}

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-test-123")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Name != "DeepSeek" {
		t.Errorf("preset identity = %q %q %q", p.ID, p.Preset, p.Name)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Errorf("preset endpoints = %q %q", p.Chat, p.Anthropic)
	}
	if p.Key != "sk-test-123" {
		t.Errorf("key = %q", p.Key)
	}
	if p.Catalog != "deepseek" {
		t.Errorf("catalog = %q, want the preset's", p.Catalog)
	}
}

func TestParseImportPresetNameOverridesAndKeyOptional(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=ollama&name=Home%20Box")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Home Box" || p.ID != "ollama" {
		t.Errorf("name/id = %q %q", p.Name, p.ID)
	}
	if p.Key != "" {
		t.Errorf("key = %q, want none", p.Key)
	}
}

func TestParseImportPresetRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=cn&key=k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region endpoints = %q %q", p.Chat, p.Anthropic)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=mars&key=k"); err == nil {
		t.Error("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn&key=k"); err == nil {
		t.Error("region on a preset without regions accepted")
	}
	if _, err := ParseImport("magpie://import?region=cn&name=X&chat=https://a.example/v1"); err == nil {
		t.Error("region without preset accepted")
	}
}

func TestParseImportUnknownPreset(t *testing.T) {
	if _, err := ParseImport("magpie://import?preset=nope&key=k"); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestParseImportCustomNeedsNameAndBase(t *testing.T) {
	for _, link := range []string{
		"magpie://import?chat=https://a.example/v1",
		"magpie://import?name=&chat=https://a.example/v1",
		"magpie://import?name=Acme",
		"magpie://import?name=Acme&key=sk-1",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("%q accepted, want rejection", link)
		}
	}
}

func TestParseImportCustomFields(t *testing.T) {
	link := "magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/&responses=https://api.acme.example/v1&anthropic=https://api.acme.example" +
		"&key=sk-1&models=" + "gpt-a%2C%20%20,,%20gpt-b%20,%20" + "&catalog=openai&website=https://acme.example&keys=https://acme.example/keys"
	p, err := ParseImport(link)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "acme-relay" || p.Name != "Acme Relay" {
		t.Errorf("id/name = %q %q", p.ID, p.Name)
	}
	if p.Chat != "https://api.acme.example/v1" {
		t.Errorf("chat = %q, want the trailing slash removed", p.Chat)
	}
	if p.Responses != "https://api.acme.example/v1" || p.Anthropic != "https://api.acme.example" {
		t.Errorf("responses/anthropic = %q %q", p.Responses, p.Anthropic)
	}
	if strings.Join(p.Models, "|") != "gpt-a|gpt-b" {
		t.Errorf("models = %q", p.Models)
	}
	if p.Catalog != "openai" || p.Website != "https://acme.example" || p.KeysURL != "https://acme.example/keys" {
		t.Errorf("catalog/website/keys = %q %q %q", p.Catalog, p.Website, p.KeysURL)
	}
}

func TestParseImportExplicitIDAndNameTruncation(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme&id=my-relay&chat=https://a.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" {
		t.Errorf("id = %q, want the explicit one", p.ID)
	}
	// 100 two-byte runes: cut to 80 runes, not 80 bytes
	p, err = ParseImport("magpie://import?name=" + strings.Repeat("%C3%A9", 100) + "&chat=https://a.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != strings.Repeat("é", 80) {
		t.Errorf("truncated name = %d runes, want the first 80", len([]rune(p.Name)))
	}
}

func TestParseImportIDRules(t *testing.T) {
	base := "&chat=https://a.example/v1"
	for _, link := range []string{
		"magpie://import?name=Magpie" + base,         // derives to magpie
		"magpie://import?name=Acme&id=magpie" + base, // explicit magpie
		"magpie://import?name=Acme&id=Acme" + base,   // explicit id not a slug
		"magpie://import?name=Acme&id=my_relay" + base,
		"magpie://import?name=!!!" + base,                     // nothing usable in the name
		"magpie://import?name=" + "%E2%9C%93%E2%9C%93" + base, // only symbols
		"magpie://import?name=Acme&id=-" + base,
	} {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%q accepted as %+v", link, p)
		}
	}
}

func TestParseImportBaseRules(t *testing.T) {
	accept := []string{
		"https://api.acme.example/v1",
		"https://api.acme.example:8443/v1/",
		"http://127.0.0.1:11434/v1",
		"http://localhost:1234/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.16.4.1/v1",
		"http://[::1]:8080/v1",
		"http://gw.local/v1",
		"http://gw.LOCAL:9000",
	}
	for _, b := range accept {
		if _, err := ParseImport("magpie://import?name=Acme&chat=" + escape(b)); err != nil {
			t.Errorf("base %q rejected: %v", b, err)
		}
	}
	reject := []string{
		"http://api.acme.example/v1", // public http
		"http://8.8.8.8/v1",          // public address
		"ftp://api.acme.example/v1",  // other scheme
		"file:///etc/passwd",         // file
		"api.acme.example/v1",        // no scheme
		"https://",                   // no host
		"https://user:pw@api.acme.example/v1",
		"https://user@api.acme.example/v1",
		"https://api.acme.example/v1?x=1",
		"https://api.acme.example/v1?",
		"https://api.acme.example/v1#frag",
		"https://api.acme.example/v1#",
	}
	for _, b := range reject {
		if _, err := ParseImport("magpie://import?name=Acme&chat=" + escape(b)); err == nil {
			t.Errorf("base %q accepted, want rejection", b)
		}
	}
}

func TestParseImportWebsiteAndKeysIgnoredUnlessHTTPS(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme&chat=https://a.example/v1&website=http://acme.example&keys=javascript:alert(1)")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "" || p.KeysURL != "" {
		t.Errorf("non-https website/keys kept: %q %q", p.Website, p.KeysURL)
	}
}

func TestParseImportWebsiteOnPresetFallsBack(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&website=http://evil.example")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "https://platform.deepseek.com" {
		t.Errorf("website = %q, want the preset's", p.Website)
	}
}

func TestParseImportKeyRules(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=Acme&chat=https://a.example/v1&key=sk-1%0Aevil",
		"magpie://import?name=Acme&chat=https://a.example/v1&key=sk-1%0D",
		"magpie://import?name=Acme&chat=https://a.example/v1&key=sk-1%00",
		"magpie://import?name=Acme&chat=https://a.example/v1&key=" + strings.Repeat("k", 4097),
	} {
		_, err := ParseImport(link)
		if err == nil {
			t.Errorf("%q accepted, want rejection", link)
			continue
		}
		if strings.Contains(err.Error(), "sk-1") {
			t.Errorf("error leaks the key: %v", err)
		}
	}
	p, err := ParseImport("magpie://import?name=Acme&chat=https://a.example/v1&key=" + strings.Repeat("k", 4096))
	if err != nil || len(p.Key) != 4096 {
		t.Errorf("4096-character key: err=%v len=%d", err, len(p.Key))
	}
}

func TestParseImportModelRules(t *testing.T) {
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, fmt.Sprintf("model-%d", i))
	}
	ok := "magpie://import?name=Acme&chat=https://a.example/v1&models=" + strings.Join(many, ",")
	if p, err := ParseImport(ok); err != nil || len(p.Models) != 200 {
		t.Errorf("200 models: err=%v n=%d", err, len(p.Models))
	}
	bad := "magpie://import?name=Acme&chat=https://a.example/v1&models=" + strings.Join(append(many, "extra"), ",")
	if _, err := ParseImport(bad); err == nil {
		t.Error("201 models accepted")
	}
	// empty entries and whitespace do not count toward the limit
	padded := "magpie://import?name=Acme&chat=https://a.example/v1&models=,,%20,a,,b,%20"
	if p, err := ParseImport(padded); err != nil || strings.Join(p.Models, "|") != "a|b" {
		t.Errorf("padded models = %q err=%v", p.Models, err)
	}
}

func TestParseImportDuplicateParameterRejected(t *testing.T) {
	if _, err := ParseImport("magpie://import?name=A&name=B&chat=https://a.example/v1"); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestParseImportErrorsDoNotLeakKey(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=Acme&key=sk-SECRET-1234%0A&chat=https://a.example/v1",
		"http://not-magpie?key=sk-SECRET-1234",
		"magpie://import?name=Acme&key=sk-SECRET-1234&chat=https://a.example/v1%zz",
		"magpie://import?name=Acme&key=sk-SECRET-1234&chat=http://public.example/v1",
	} {
		_, err := ParseImport(link)
		if err == nil {
			t.Errorf("%q accepted", link)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error leaks the key: %v", err)
		}
	}
}

// escape percent-encodes the characters that would otherwise end a query value.
func escape(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "?", "%3F", "+", "%2B", " ", "%20").Replace(s)
}
