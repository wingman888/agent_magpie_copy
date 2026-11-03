package provider

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q) rejected: %v", link, err)
	}
	return p
}

func mustReject(t *testing.T, link string) {
	t.Helper()
	p, err := ParseImport(link)
	if err == nil {
		t.Fatalf("ParseImport(%q) accepted: %+v", link, p)
	}
	// a rejected link must not come back half-filled
	if !reflect.DeepEqual(p, Provider{}) {
		t.Fatalf("ParseImport(%q) returned a partial result with its error: %+v", link, p)
	}
}

func TestImportPreset(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&key=sk-1")
	if p.ID != "deepseek" || p.Name != "DeepSeek" || p.Preset != "deepseek" {
		t.Fatalf("preset identity wrong: %+v", p)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Fatalf("preset endpoints wrong: %+v", p)
	}
	if p.Key != "sk-1" {
		t.Fatalf("key = %q", p.Key)
	}
	if p.KeysURL == "" || p.Website == "" {
		t.Fatalf("preset website/keys not carried over: %+v", p)
	}
}

func TestImportPresetNameOverrideAndID(t *testing.T) {
	p := mustParse(t, "magpie://import?preset=deepseek&name=My%20DS")
	if p.Name != "My DS" || p.ID != "deepseek" {
		t.Fatalf("name override wrong: name=%q id=%q", p.Name, p.ID)
	}
	if p.Key != "" {
		t.Fatalf("no key given but key = %q", p.Key)
	}
}

func TestImportPresetRegion(t *testing.T) {
	cn := mustParse(t, "magpie://import?preset=yylx&region=cn&key=sk-1")
	if cn.Chat != "https://cn.yylx.io/v1" || cn.Anthropic != "https://cn.yylx.io" {
		t.Fatalf("region cn endpoints wrong: %+v", cn)
	}
	def := mustParse(t, "magpie://import?preset=yylx")
	if def.Chat != "https://app.yylx.io/v1" {
		t.Fatalf("default region should be the first one, got %q", def.Chat)
	}
	mustReject(t, "magpie://import?preset=yylx&region=mars")
	mustReject(t, "magpie://import?preset=deepseek&region=cn")
	mustReject(t, "magpie://import?region=cn&name=X&chat=https://a.example/v1")
}

func TestImportUnknownPreset(t *testing.T) {
	mustReject(t, "magpie://import?preset=nope&key=sk-1")
}

func TestImportCustom(t *testing.T) {
	p := mustParse(t, "magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-x")
	if p.ID != "acme-relay" || p.Name != "Acme Relay" {
		t.Fatalf("custom identity wrong: %+v", p)
	}
	if p.Chat != "https://api.acme.example/v1" || p.Preset != "" {
		t.Fatalf("custom endpoint wrong: %+v", p)
	}
	if p.Key != "sk-x" {
		t.Fatalf("key = %q", p.Key)
	}
}

func TestImportEquivalentSchemes(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=Acme&chat=https://a.example/v1",
		"magpie:import?name=Acme&chat=https://a.example/v1",
		"magpie:///import?name=Acme&chat=https://a.example/v1",
		"magpie://import/?name=Acme&chat=https://a.example/v1",
		"MAGPIE://import?name=Acme&chat=https://a.example/v1",
		"Magpie:///IMPORT?name=Acme&chat=https://a.example/v1",
		"  magpie://import?name=Acme&chat=https://a.example/v1  ",
	} {
		p := mustParse(t, link)
		if p.ID != "acme" || p.Chat != "https://a.example/v1" {
			t.Errorf("ParseImport(%q) = %+v", link, p)
		}
	}
}

func TestImportRejectsOtherSchemesAndActions(t *testing.T) {
	for _, link := range []string{
		"https://import?name=Acme&chat=https://a.example/v1",
		"file:///import?name=Acme&chat=https://a.example/v1",
		"magpie://delete?name=Acme&chat=https://a.example/v1",
		"magpie:delete?name=Acme&chat=https://a.example/v1",
		"magpie:///etc?name=Acme&chat=https://a.example/v1",
		"magpie://import/extra?name=Acme&chat=https://a.example/v1",
		"magpie://?name=Acme&chat=https://a.example/v1",
		"not a link",
		"",
	} {
		mustReject(t, link)
	}
}

func TestImportRejectsMissingNameOrBase(t *testing.T) {
	mustReject(t, "magpie://import?chat=https://a.example/v1")
	mustReject(t, "magpie://import?name=Acme")
	mustReject(t, "magpie://import?name=Acme&key=sk-1")
}

func TestImportRejectsPublicHTTP(t *testing.T) {
	mustReject(t, "magpie://import?name=Acme&chat=http://api.example.com/v1")
	mustReject(t, "magpie://import?name=Acme&anthropic=http://8.8.8.8")
}

func TestImportAcceptsLocalHTTP(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:8000/v1",
		"http://localhost:1234/v1",
		"http://192.168.1.5:11434/v1",
		"http://10.0.0.2/v1",
		"http://172.16.4.4/v1",
		"http://printer.local/v1",
		"http://[::1]:8080/v1",
	} {
		p := mustParse(t, "magpie://import?name=Local&chat="+url.QueryEscape(base))
		if p.Chat != base {
			t.Errorf("local base %q came back as %q", base, p.Chat)
		}
	}
}

func TestImportRejectsBaseWithCredentialsQueryOrFragment(t *testing.T) {
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("https://user:pw@api.example.com/v1"))
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("https://api.example.com/v1?x=1"))
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("https://api.example.com/v1#frag"))
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("https://api.example.com/v1?"))
}

func TestImportRejectsFileScheme(t *testing.T) {
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("file:///etc/passwd"))
	mustReject(t, "magpie://import?name=Acme&chat="+url.QueryEscape("ftp://a.example/v1"))
}

func TestImportTrailingSlashRemoved(t *testing.T) {
	p := mustParse(t, "magpie://import?name=Acme&anthropic=https://a.example/&chat=https://a.example/v1/")
	if p.Anthropic != "https://a.example" || p.Chat != "https://a.example/v1" {
		t.Fatalf("trailing slash not removed: %+v", p)
	}
}

func TestImportIDRules(t *testing.T) {
	mustReject(t, "magpie://import?id=magpie&name=Acme&chat=https://a.example/v1")
	mustReject(t, "magpie://import?name=magpie&chat=https://a.example/v1") // derives to magpie
	mustReject(t, "magpie://import?name=!!!&chat=https://a.example/v1")    // no usable characters
	mustReject(t, "magpie://import?name=鱼鱼连线&chat=https://a.example/v1")
	mustReject(t, "magpie://import?id=Acme&name=Acme&chat=https://a.example/v1") // explicit id not a slug
	mustReject(t, "magpie://import?id=my_relay&name=X&chat=https://a.example/v1")
	mustReject(t, "magpie://import?id=codex&name=X&chat=https://a.example/v1") // a sign-in agent's id

	p := mustParse(t, "magpie://import?id=my-relay&name=Whatever&chat=https://a.example/v1")
	if p.ID != "my-relay" {
		t.Fatalf("explicit id = %q", p.ID)
	}
	if p := mustParse(t, "magpie://import?name=My%20Relay&chat=https://a.example/v1"); p.ID != "my-relay" {
		t.Fatalf("derived id = %q", p.ID)
	}
	if p := mustParse(t, "magpie://import?name=%20--Acme%20Relay--!&chat=https://a.example/v1"); p.ID != "acme-relay" {
		t.Fatalf("derived id with symbols = %q", p.ID)
	}
	if p := mustParse(t, "magpie://import?preset=deepseek&id=deepseek-2"); p.ID != "deepseek-2" {
		t.Fatalf("explicit id with preset = %q", p.ID)
	}
	if p := mustParse(t, "magpie://import?preset=deepseek&name=鱼&chat=https://a.example/v1"); p.ID != "deepseek" {
		t.Fatalf("preset id should be kept when name is not a slug: %q", p.ID)
	}
}

func TestImportKeyRules(t *testing.T) {
	mustReject(t, "magpie://import?name=Acme&chat=https://a.example/v1&key=sk%0A1")
	mustReject(t, "magpie://import?name=Acme&chat=https://a.example/v1&key=sk%0D%0A")
	mustReject(t, "magpie://import?name=Acme&chat=https://a.example/v1&key="+strings.Repeat("a", 4097))

	p := mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&key="+strings.Repeat("a", 4096))
	if len(p.Key) != 4096 {
		t.Fatalf("4096-character key was not kept: %d", len(p.Key))
	}
}

func TestImportNameTruncated(t *testing.T) {
	long := strings.Repeat("a", 79) + "é" + "xyz" // 83 characters
	p := mustParse(t, "magpie://import?name="+url.QueryEscape(long)+"&chat=https://a.example/v1")
	if n := utf8.RuneCountInString(p.Name); n != 80 {
		t.Fatalf("name has %d characters, want 80", n)
	}
	if !strings.HasPrefix(p.Name, strings.Repeat("a", 79)+"é") {
		t.Fatalf("name not cut from the front: %q", p.Name)
	}
}

func TestImportModels(t *testing.T) {
	p := mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&models=a,%20,%20b%20,,%20c")
	if !reflect.DeepEqual(p.Models, []string{"a", "b", "c"}) {
		t.Fatalf("models = %q", p.Models)
	}

	var ids []string
	for i := 0; i < 250; i++ {
		ids = append(ids, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("0", i/26))
	}
	p = mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&models="+url.QueryEscape(strings.Join(ids, ",")))
	if len(p.Models) != 200 {
		t.Fatalf("got %d models, want 200", len(p.Models))
	}
}

func TestImportWebsiteAndKeysHTTPSOnly(t *testing.T) {
	p := mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&website=http://evil.example&keys=ftp://x.example/k")
	if p.Website != "" || p.KeysURL != "" {
		t.Fatalf("non-https website/keys kept: %q %q", p.Website, p.KeysURL)
	}
	p = mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&website=https://acme.example/&keys=https://acme.example/keys")
	if p.Website != "https://acme.example" || p.KeysURL != "https://acme.example/keys" {
		t.Fatalf("https website/keys wrong: %q %q", p.Website, p.KeysURL)
	}

	// a preset's own links survive an ignored value in the link
	d := mustParse(t, "magpie://import?preset=deepseek&website=javascript:alert(1)")
	if d.Website != "https://platform.deepseek.com" {
		t.Fatalf("preset website lost: %q", d.Website)
	}
}

func TestImportRejectsDuplicateParameters(t *testing.T) {
	mustReject(t, "magpie://import?name=A&name=B&chat=https://a.example/v1")
}

func TestImportWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&key=sk-1")
	mustReject(t, "magpie://import?name=Acme&chat=http://api.example.com/v1")
	if _, err := os.Stat(filepath.Join(dir, "magpie", "providers.json")); !os.IsNotExist(err) {
		t.Fatalf("ParseImport wrote a providers file (stat err %v)", err)
	}
}
