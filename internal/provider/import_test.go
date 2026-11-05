package provider

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func mustImport(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): %v", link, err)
	}
	return p
}

func mustRefuse(t *testing.T, link string) {
	t.Helper()
	if p, err := ParseImport(link); err == nil {
		t.Fatalf("ParseImport(%q) accepted, want refusal: %+v", link, p)
	}
}

func TestImportPresetWithKey(t *testing.T) {
	p := mustImport(t, "magpie://import?preset=deepseek&key=sk-abc")
	if p.ID != "deepseek" || p.Name != "DeepSeek" || p.Preset != "deepseek" {
		t.Fatalf("got id=%q name=%q preset=%q", p.ID, p.Name, p.Preset)
	}
	if p.Chat != "https://api.deepseek.com/v1" || p.Anthropic != "https://api.deepseek.com/anthropic" {
		t.Fatalf("preset endpoints not used: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	if p.Key != "sk-abc" {
		t.Fatalf("key = %q", p.Key)
	}
	if p.Catalog != "deepseek" || p.KeysURL == "" {
		t.Fatalf("preset catalog/keys missing: %q %q", p.Catalog, p.KeysURL)
	}
}

func TestImportPresetWithoutKeyAndNameOverride(t *testing.T) {
	p := mustImport(t, "magpie://import?preset=groq&name=Groq%20Team")
	if p.Key != "" {
		t.Fatalf("key should be empty, got %q", p.Key)
	}
	if p.ID != "groq" || p.Name != "Groq Team" {
		t.Fatalf("id=%q name=%q", p.ID, p.Name)
	}
}

func TestImportPresetRegion(t *testing.T) {
	p := mustImport(t, "magpie://import?preset=yylx&region=cn&key=sk-x")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Fatalf("region endpoints: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	// no region: the preset's default endpoints
	p = mustImport(t, "magpie://import?preset=yylx")
	if p.Chat != "https://app.yylx.io/v1" {
		t.Fatalf("default region chat = %q", p.Chat)
	}
	// region names are matched without regard to case
	p = mustImport(t, "magpie://import?preset=yylx&region=GLOBAL")
	if p.Chat != "https://global.yylx.io/v1" {
		t.Fatalf("GLOBAL chat = %q", p.Chat)
	}
}

func TestImportRefusesUnknownPresetAndRegion(t *testing.T) {
	mustRefuse(t, "magpie://import?preset=nope&key=sk-x")
	mustRefuse(t, "magpie://import?preset=yylx&region=mars")
	mustRefuse(t, "magpie://import?preset=deepseek&region=cn")
	mustRefuse(t, "magpie://import?region=cn&name=x&chat=https://a.example/v1")
}

func TestImportCustomNameAndBase(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-1")
	if p.ID != "acme-relay" || p.Name != "Acme Relay" {
		t.Fatalf("id=%q name=%q", p.ID, p.Name)
	}
	if p.Preset != "" {
		t.Fatalf("custom provider has preset %q", p.Preset)
	}
	if p.Chat != "https://api.acme.example/v1" || p.Responses != "" || p.Anthropic != "" {
		t.Fatalf("bases: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	if p.Key != "sk-1" {
		t.Fatalf("key = %q", p.Key)
	}
}

func TestImportDerivesID(t *testing.T) {
	cases := map[string]string{
		"My Relay":       "my-relay",
		"  Acme   Relay ": "acme-relay",
		"AI/Hub 2.0":     "ai-hub-2-0",
	}
	for name, want := range cases {
		p := mustImport(t, "magpie://import?name="+strings.ReplaceAll(strings.TrimSpace(name), " ", "+")+"&chat=https://x.example/v1")
		if p.ID != want {
			t.Errorf("name %q: id = %q, want %q", name, p.ID, want)
		}
	}
}

func TestImportRefusesNameWithoutUsableCharacters(t *testing.T) {
	mustRefuse(t, "magpie://import?name=%E9%B1%BC&chat=https://x.example/v1")       // 鱼
	mustRefuse(t, "magpie://import?name=!!!&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?name=%20%20&chat=https://x.example/v1")
}

func TestImportExplicitIDMustBeSlug(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Anything&id=acme-2&chat=https://x.example/v1")
	if p.ID != "acme-2" {
		t.Fatalf("id = %q", p.ID)
	}
	mustRefuse(t, "magpie://import?name=Acme&id=Acme&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?name=Acme&id=acme_relay&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?name=Acme&id=acme%20relay&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?name=Acme&id=magpie&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?name=Acme&id=MAGPIE&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?preset=deepseek&id=Deep_Seek")
}

func TestImportPresetIDCannotBeMagpie(t *testing.T) {
	// the name of a preset may be "magpie" only through an explicit id
	mustRefuse(t, "magpie://import?name=magpie&chat=https://x.example/v1")
}

func TestImportSchemeVariants(t *testing.T) {
	base := "name=Acme&chat=https://x.example/v1"
	for _, link := range []string{
		"magpie://import?" + base,
		"magpie:import?" + base,
		"magpie:///import?" + base,
		"magpie://import/?" + base,
		"MAGPIE://import?" + base,
		"Magpie:import?" + base,
		"  magpie://import?" + base + "  ",
	} {
		p := mustImport(t, link)
		if p.ID != "acme" {
			t.Errorf("%q: id = %q", link, p.ID)
		}
	}
}

func TestImportRefusesOtherSchemesAndActions(t *testing.T) {
	base := "name=Acme&chat=https://x.example/v1"
	for _, link := range []string{
		"https://import?" + base,
		"file:///import?" + base,
		"http://import?" + base,
		"magpie://delete?" + base,
		"magpie:///delete?" + base,
		"magpie://import/extra?" + base,
		"magpie://imports?" + base,
		"magpie://",
		"magpie",
		"",
		"https://usemagpie.ai/import#" + base,
	} {
		mustRefuse(t, link)
	}
}

func TestImportRefusesMissingNameOrBase(t *testing.T) {
	mustRefuse(t, "magpie://import?chat=https://x.example/v1")       // no name
	mustRefuse(t, "magpie://import?name=Acme")                       // no base
	mustRefuse(t, "magpie://import?name=Acme&key=sk-1")              // no base
	mustRefuse(t, "magpie://import?name=Acme&chat=")                 // empty base
	mustRefuse(t, "magpie://import?name=&chat=https://x.example/v1") // empty name
}

func TestImportBaseSchemeRules(t *testing.T) {
	// refused: plain http to a public host, file://, and anything not https
	for _, base := range []string{
		"http://api.acme.example/v1",
		"file:///etc/passwd",
		"ftp://x.example/v1",
		"x.example/v1",
		"//x.example/v1",
		"https:///v1",
	} {
		mustRefuse(t, "magpie://import?name=Acme&chat="+base)
	}
}

func TestImportLocalHTTPAccepted(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:1234/v1",
		"http://127.1.2.3/v1",
		"http://10.1.2.3:8080/v1",
		"http://192.168.1.5/v1",
		"http://172.16.0.9/v1",
		"http://gpu-box.local:8000/v1",
		"http://[::1]:9000/v1",
		"http://[fd00::5]/v1",
	} {
		p := mustImport(t, "magpie://import?name=Local&chat="+base)
		if p.Chat != base {
			t.Errorf("%s: chat = %q", base, p.Chat)
		}
	}
	mustRefuse(t, "magpie://import?name=Local&chat=http://8.8.8.8/v1")
	mustRefuse(t, "magpie://import?name=Local&chat=http://example.com/v1")
}

func TestImportRefusesUserinfoQueryAndFragmentInBases(t *testing.T) {
	for _, base := range []string{
		"https://user:pw@api.acme.example/v1",
		"https://user@api.acme.example/v1",
		"https://api.acme.example/v1?x=1",
		"https://api.acme.example/v1?",
		"https://api.acme.example/v1#frag",
		"https://api.acme.example/v1#",
	} {
		mustRefuse(t, "magpie://import?name=Acme&chat="+base)
	}
}

func TestImportBaseTrailingSlashRemoved(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1/&anthropic=https://x.example/")
	if p.Chat != "https://x.example/v1" || p.Anthropic != "https://x.example" {
		t.Fatalf("chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
}

func TestImportMultipleBases(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme&chat=https://a.example/v1&responses=https://a.example/v1&anthropic=https://a.example")
	if p.Speaks()[0] != Chat || len(p.Speaks()) != 3 {
		t.Fatalf("speaks = %v", p.Speaks())
	}
}

func TestImportPresetOverriddenBase(t *testing.T) {
	p := mustImport(t, "magpie://import?preset=openai&chat=https://proxy.example/v1")
	if p.Chat != "https://proxy.example/v1" {
		t.Fatalf("chat = %q", p.Chat)
	}
	if p.Responses != "https://api.openai.com/v1" {
		t.Fatalf("responses should keep the preset value, got %q", p.Responses)
	}
}

func TestImportModelsCleaned(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&models=gpt-5.5%2C%20%2C+claude-sonnet-5%2C+gpt-5.5%2C%20")
	if len(p.Models) != 2 || p.Models[0] != "gpt-5.5" || p.Models[1] != "claude-sonnet-5" {
		t.Fatalf("models = %q", p.Models)
	}
}

func TestImportModelsCappedAt200(t *testing.T) {
	var ids []string
	for i := 0; i < 250; i++ {
		ids = append(ids, "m"+strings.Repeat("x", i%3)+"-"+strconv.Itoa(i))
	}
	link := "magpie://import?name=Acme&chat=https://x.example/v1&models=" + strings.Join(ids, ",")
	p := mustImport(t, link)
	if len(p.Models) != 200 {
		t.Fatalf("got %d models, want 200", len(p.Models))
	}
	if p.Models[0] != ids[0] || p.Models[199] != ids[199] {
		t.Fatalf("models not kept in order")
	}
}

func TestImportNameTruncatedTo80(t *testing.T) {
	long := strings.Repeat("a", 85)
	p := mustImport(t, "magpie://import?name="+long+"&chat=https://x.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Fatalf("name length = %d", len([]rune(p.Name)))
	}
	if p.ID != strings.Repeat("a", 80) {
		t.Fatalf("id = %q", p.ID)
	}
}

func TestImportKeyRules(t *testing.T) {
	mustRefuse(t, "magpie://import?name=Acme&chat=https://x.example/v1&key=sk-1%0Aevil")
	mustRefuse(t, "magpie://import?name=Acme&chat=https://x.example/v1&key=sk-1%0D")
	mustRefuse(t, "magpie://import?name=Acme&chat=https://x.example/v1&key=sk%09x")
	long := strings.Repeat("k", 4097)
	mustRefuse(t, "magpie://import?name=Acme&chat=https://x.example/v1&key="+long)
	ok := strings.Repeat("k", 4096)
	if p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&key="+ok); len(p.Key) != 4096 {
		t.Fatalf("4096-character key rejected or altered")
	}
}

func TestImportErrorsDoNotEchoKey(t *testing.T) {
	const secret = "sk-SECRET-VALUE-123"
	links := []string{
		"magpie://import?name=Acme&chat=https://x.example/v1&key=" + secret + "%0A",
		"magpie://import?name=Acme&chat=https://x.example/v1&key=" + secret + "%0A%zz",
		"magpie://import?name=Acme&chat=http://x.example/v1&key=" + secret,
		"magpie://import?name=Acme&chat=https://u:p@x.example/v1&key=" + secret,
		"magpie://import?name=Acme&chat=https://x.example/v1&key=" + secret + "&chat=https://y.example/v1",
		"magpie://import?name=Acme&chat=https://x.example/v1&key=" + secret + "%zz",
		"magpie://delete?key=" + secret,
		"magpie://import?preset=nope&key=" + secret,
		"magpie://import?name=Acme%0A&key=" + secret,
	}
	for _, l := range links {
		_, err := ParseImport(l)
		if err == nil {
			t.Fatalf("accepted %q", l)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoes the key: %v", err)
		}
	}
}

func TestImportWebsiteAndKeysHTTPSOnly(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&website=http://acme.example&keys=javascript:alert(1)")
	if p.Website != "" || p.KeysURL != "" {
		t.Fatalf("non-https website/keys kept: %q %q", p.Website, p.KeysURL)
	}
	p = mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&website=https://acme.example/&keys=https://acme.example/keys")
	if p.Website != "https://acme.example" || p.KeysURL != "https://acme.example/keys" {
		t.Fatalf("https website/keys: %q %q", p.Website, p.KeysURL)
	}
	// an ignored website on a preset leaves the preset's value alone
	p = mustImport(t, "magpie://import?preset=deepseek&website=http://evil.example")
	if p.Website != "https://platform.deepseek.com" {
		t.Fatalf("preset website changed: %q", p.Website)
	}
}

func TestImportCatalogCarried(t *testing.T) {
	p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&catalog=openai")
	if p.Catalog != "openai" {
		t.Fatalf("catalog = %q", p.Catalog)
	}
}

func TestImportRefusesRepeatedParameter(t *testing.T) {
	mustRefuse(t, "magpie://import?name=A&name=B&chat=https://x.example/v1")
	mustRefuse(t, "magpie://import?preset=deepseek&preset=openai")
}

func TestImportDoesNotSave(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1&key=sk-1")
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatalf("ParseImport wrote %s (err=%v)", Path(), err)
	}
}

func TestSaveImportedAcceptsKeyless(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := mustImport(t, "magpie://import?name=Acme&chat=https://x.example/v1")
	if err := Save(p); err == nil {
		t.Fatalf("Save accepted a keyless provider")
	}
	if err := SaveImported(p); err != nil {
		t.Fatalf("SaveImported: %v", err)
	}
	saved, err := Find("acme")
	if err != nil {
		t.Fatalf("Find after SaveImported: %v", err)
	}
	if saved.Ready() {
		t.Fatalf("keyless provider reports ready")
	}
}
