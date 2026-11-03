package provider

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// importIsolate points the config dir at a temp dir so a test that accidentally
// saved something would be visible; ParseImport must leave it empty.
func importIsolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func mustParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): unexpected error: %v", link, err)
	}
	return p
}

func TestImportScheme(t *testing.T) {
	if Scheme != "magpie" {
		t.Fatalf("Scheme = %q", Scheme)
	}
}

func TestImportLinkForms(t *testing.T) {
	importIsolate(t)
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  Magpie://IMPORT?preset=deepseek  ",
	} {
		p := mustParse(t, link)
		if p.ID != "deepseek" || p.Preset != "deepseek" {
			t.Errorf("%q: got id %q preset %q", link, p.ID, p.Preset)
		}
	}
}

func TestImportPresetWithModelsAndKey(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	pr := Preset("deepseek")
	if p.ID != "deepseek" || p.Key != "sk-abc" {
		t.Errorf("id %q key %q", p.ID, p.Key)
	}
	if strings.Join(p.Models, "|") != "a|b" {
		t.Errorf("models = %q", p.Models)
	}
	if p.Chat != pr.Chat || p.Responses != pr.Responses || p.Anthropic != pr.Anthropic {
		t.Errorf("endpoints not taken from preset: %+v", p)
	}
	if p.Name != pr.Name {
		t.Errorf("name = %q, want preset name %q", p.Name, pr.Name)
	}
}

func TestImportPresetIgnoresEndpointParams(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=deepseek&chat=https://evil.example/v1&website=javascript:x&catalog=bad!")
	pr := Preset("deepseek")
	if p.Chat != pr.Chat || p.Website != pr.Website {
		t.Errorf("preset endpoints overridden: chat %q website %q", p.Chat, p.Website)
	}
}

func TestImportPresetNameOverride(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=deepseek&name=Work%20DS")
	if p.Name != "Work DS" || p.ID != "deepseek" {
		t.Errorf("name %q id %q", p.Name, p.ID)
	}
}

func TestImportRegion(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=yylx&region=cn")
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Errorf("region cn endpoints: chat %q anthropic %q", p.Chat, p.Anthropic)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Error("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Error("region on a preset without regions accepted")
	}
}

func TestImportCustom(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)")
	if p.ID != "my-relay" || p.Name != "My Relay" {
		t.Errorf("id %q name %q", p.ID, p.Name)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("chat = %q, trailing slash not stripped", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Errorf("anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Errorf("https website dropped: %q", p.Website)
	}
	if p.KeysURL != "" {
		t.Errorf("javascript keys URL kept: %q", p.KeysURL)
	}
	if p.Preset != "" || p.Key != "" {
		t.Errorf("unexpected preset %q key %q", p.Preset, p.Key)
	}
}

func TestImportCustomCatalogAndKeysHTTPS(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?name=Acme&responses=https://api.acme.example/v1&catalog=fireworks-ai&keys=https://acme.example/keys")
	if p.Catalog != "fireworks-ai" || p.KeysURL != "https://acme.example/keys" {
		t.Errorf("catalog %q keys %q", p.Catalog, p.KeysURL)
	}
	p = mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&catalog=Bad%20Id")
	if p.Catalog != "" {
		t.Errorf("invalid catalog kept: %q", p.Catalog)
	}
}

func TestImportCustomFullExample(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&anthropic=https://api.acme.example&key=sk-x&models=gpt-5.5,claude-sonnet-5")
	if p.ID != "acme-relay" || p.Key != "sk-x" || strings.Join(p.Models, ",") != "gpt-5.5,claude-sonnet-5" {
		t.Errorf("got %+v", p)
	}
}

func TestImportIDRules(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?name=Anything&id=Custom%20ID&chat=https://a.example/v1")
	if p.ID != "custom-id" {
		t.Errorf("explicit id = %q", p.ID)
	}
	for _, link := range []string{
		"magpie://import?name=%E2%80%94&chat=https://a.example/v1", // em dash: empty slug
		"magpie://import?name=magpie&chat=https://a.example/v1",    // reserved
		"magpie://import?name=Ok&id=magpie&chat=https://a.example/v1",
		"magpie://import?name=Ok&id=---&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("%q accepted", link)
		}
	}
}

func TestImportNameTruncated(t *testing.T) {
	importIsolate(t)
	long := strings.Repeat("a", 100)
	p := mustParse(t, "magpie://import?name="+long+"&chat=https://a.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Errorf("name length %d, want 80", len([]rune(p.Name)))
	}
}

func TestImportNoKeyIsAllowed(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=openai")
	if p.Key != "" || p.Ready() {
		t.Errorf("key %q ready %v", p.Key, p.Ready())
	}
}

func TestImportLocalHTTPAccepted(t *testing.T) {
	importIsolate(t)
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.20.1.1/v1",
		"http://[::1]:9000/v1",
		"http://box.local/v1",
	} {
		p := mustParse(t, "magpie://import?name=Local&chat="+base)
		if p.Chat != base {
			t.Errorf("%s: chat = %q", base, p.Chat)
		}
	}
}

func TestImportRejects(t *testing.T) {
	importIsolate(t)
	cases := map[string]string{
		"https url":           "https://usemagpie.ai/import?preset=deepseek",
		"other action":        "magpie://delete?id=x",
		"action with host":    "magpie://import.evil/?preset=deepseek",
		"no params":           "magpie://import",
		"unknown preset":      "magpie://import?preset=nope",
		"name only":           "magpie://import?name=Relay",
		"unknown param":       "magpie://import?name=Relay&chat=https://a.example/v1&base=x",
		"repeated param":      "magpie://import?preset=deepseek&preset=openai",
		"plain http remote":   "magpie://import?name=R&chat=http://relay.example/v1",
		"userinfo":            "magpie://import?name=R&chat=https://user:pw@relay.example/v1",
		"file scheme":         "magpie://import?name=R&chat=file:///etc/passwd",
		"query in base":       "magpie://import?name=R&chat=https://relay.example/v1?x=1",
		"fragment in base":    "magpie://import?name=R&chat=https://relay.example/v1%23frag",
		"reserved name":       "magpie://import?name=magpie&chat=https://a.example/v1",
		"symbol-only name":    "magpie://import?name=%E2%80%94&chat=https://a.example/v1",
		"newline in key":      "magpie://import?preset=deepseek&key=sk-a%0Aevil",
		"space in key":        "magpie://import?preset=deepseek&key=sk-a%20b",
		"oversize key":        "magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097),
		"too many models":     "magpie://import?preset=deepseek&models=" + manyModelsQuery(201),
		"control in model":    "magpie://import?preset=deepseek&models=a%0Ab",
		"fragment in link":    "magpie://import?preset=deepseek#x",
		"not magpie":          "https://example.com/magpie://import?preset=deepseek",
		"region without any":  "magpie://import?region=cn",
		"no base url":         "magpie://import?name=Relay&website=https://relay.example",
		"empty base path":     "magpie://import?name=R&chat=https://",
		"control in name":     "magpie://import?name=A%0AB&chat=https://a.example/v1",
		"plain http dns host": "magpie://import?name=R&anthropic=http://api.example.com",
	}
	for what, link := range cases {
		p, err := ParseImport(link)
		if err == nil {
			t.Errorf("%s: %q accepted as %+v", what, link, p)
		}
	}
}

func TestImportKeyLengthBoundary(t *testing.T) {
	importIsolate(t)
	mustParse(t, "magpie://import?preset=deepseek&key="+strings.Repeat("k", 4096))
}

func TestImportModelsBoundaryAndCleaning(t *testing.T) {
	importIsolate(t)
	p := mustParse(t, "magpie://import?preset=deepseek&models="+manyModelsQuery(200))
	if len(p.Models) != 200 {
		t.Errorf("got %d models, want 200", len(p.Models))
	}
	p = mustParse(t, "magpie://import?preset=deepseek&models=%20a%20,,%20,b,")
	if strings.Join(p.Models, "|") != "a|b" {
		t.Errorf("models = %q", p.Models)
	}
}

// ParseImport must never write the config file or touch the network.
func TestImportHasNoSideEffects(t *testing.T) {
	dir := importIsolate(t)
	for _, link := range []string{
		"magpie://import?preset=deepseek&key=sk-abc",
		"magpie://import?name=My%20Relay&chat=https://relay.example/v1",
	} {
		mustParse(t, link)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("ParseImport wrote %s (stat err %v)", dir, err)
	}
}

func manyModelsQuery(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "m" + strconv.Itoa(i)
	}
	return strings.Join(parts, ",")
}
