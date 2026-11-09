package provider

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func impTestParse(t *testing.T, link string) Provider {
	t.Helper()
	p, err := ParseImport(link)
	if err != nil {
		t.Fatalf("ParseImport(%q): unexpected error: %v", link, err)
	}
	return p
}

func TestImportAcceptsSchemeVariants(t *testing.T) {
	base := "?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-test"
	links := []string{
		"magpie://import" + base,
		"magpie:import" + base,
		"magpie:///import" + base,
		"MAGPIE://import" + base,
		"Magpie:import" + base,
		"magpie://import/" + base,
	}
	for _, l := range links {
		p := impTestParse(t, l)
		if p.ID != "acme-relay" || p.Name != "Acme Relay" || p.Chat != "https://api.acme.example/v1" || p.Key != "sk-test" {
			t.Errorf("ParseImport(%q) = %+v", l, p)
		}
	}
}

func TestImportRejectsOtherSchemesAndActions(t *testing.T) {
	for _, l := range []string{
		"https://import?name=A&chat=https://a.example/v1",
		"file://import?name=A&chat=https://a.example/v1",
		"magpie://delete?name=A&chat=https://a.example/v1",
		"magpie://import/extra?name=A&chat=https://a.example/v1",
		"magpie://host/import?name=A&chat=https://a.example/v1",
		"",
		"not a link",
	} {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("ParseImport(%q) accepted, want rejection", l)
		}
	}
}

func TestImportPresetAndRegion(t *testing.T) {
	p := impTestParse(t, "magpie://import?preset=deepseek&key=sk-x")
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Name != "DeepSeek" || p.Chat != "https://api.deepseek.com/v1" {
		t.Errorf("preset import = %+v", p)
	}

	p = impTestParse(t, "magpie://import?preset=yylx&region=cn&name=My%20YYLX")
	if p.Name != "My YYLX" || p.ID != "yylx" || p.Chat != "https://cn.yylx.io/v1" {
		t.Errorf("region import = %+v", p)
	}

	p = impTestParse(t, "magpie://import?preset=yylx")
	if p.Chat != "https://app.yylx.io/v1" {
		t.Errorf("default region import = %+v", p)
	}

	p = impTestParse(t, "magpie://import?preset=opencode-go&key=k")
	if p.Chat != "https://opencode.ai/zen/go/v1" || p.Anthropic != "https://opencode.ai/zen/go" {
		t.Errorf("opencode-go endpoints = %+v", p)
	}
}

func TestImportRejectsBadPresetAndRegion(t *testing.T) {
	for _, l := range []string{
		"magpie://import?preset=nosuch&key=k",
		"magpie://import?preset=yylx&region=mars",
		"magpie://import?preset=deepseek&region=cn",
		"magpie://import?region=cn&name=X&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("ParseImport(%q) accepted, want rejection", l)
		}
	}
}

func TestImportRequiresNameAndBase(t *testing.T) {
	for _, l := range []string{
		"magpie://import?chat=https://a.example/v1&key=k",
		"magpie://import?name=A&key=k",
		"magpie://import?name=%20%20&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("ParseImport(%q) accepted, want rejection", l)
		}
	}
}

func TestImportBaseURLRules(t *testing.T) {
	bad := []string{
		"http://api.acme.example/v1",
		"https://user:pw@api.acme.example/v1",
		"https://api.acme.example/v1?x=1",
		"https://api.acme.example/v1#frag",
		"file:///etc/passwd",
		"ftp://api.acme.example/v1",
		"api.acme.example/v1",
	}
	for _, b := range bad {
		l := "magpie://import?name=A&chat=" + b
		if _, err := ParseImport(l); err == nil {
			t.Errorf("base %q accepted, want rejection", b)
		}
	}

	good := [][2]string{
		{"https://api.acme.example/v1/", "https://api.acme.example/v1"},
		{"http://localhost:8080/v1", "http://localhost:8080/v1"},
		{"http://127.0.0.1:7890/v1", "http://127.0.0.1:7890/v1"},
		{"http://192.168.1.20:11434/v1", "http://192.168.1.20:11434/v1"},
		{"http://10.0.0.5/v1", "http://10.0.0.5/v1"},
		{"http://gw.office.local/v1", "http://gw.office.local/v1"},
		{"http://[::1]:9000/v1", "http://[::1]:9000/v1"},
	}
	for _, c := range good {
		p := impTestParse(t, "magpie://import?name=A&chat="+c[0])
		if p.Chat != c[1] {
			t.Errorf("base %q: got %q, want %q", c[0], p.Chat, c[1])
		}
	}
}

func TestImportIDRules(t *testing.T) {
	for _, l := range []string{
		"magpie://import?name=magpie&chat=https://a.example/v1",
		"magpie://import?name=%2B%2B%2B&chat=https://a.example/v1",
		"magpie://import?name=A&id=magpie&chat=https://a.example/v1",
		"magpie://import?name=A&id=Bad_ID&chat=https://a.example/v1",
		"magpie://import?name=A&id=has%20space&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(l); err == nil {
			t.Errorf("ParseImport(%q) accepted, want rejection", l)
		}
	}
	p := impTestParse(t, "magpie://import?name=A&id=my-relay&chat=https://a.example/v1")
	if p.ID != "my-relay" {
		t.Errorf("explicit id = %q", p.ID)
	}
	p = impTestParse(t, "magpie://import?name=My%20Relay&chat=https://a.example/v1")
	if p.ID != "my-relay" {
		t.Errorf("derived id = %q", p.ID)
	}
}

func TestImportSlugDerivation(t *testing.T) {
	cases := [][2]string{
		{"Acme Relay", "acme-relay"},
		{"My Relay", "my-relay"},
		{"  --Foo!! ", "foo"},
		{"302.AI", "302-ai"},
		{"鱼鱼 relay", "relay"},
	}
	for _, c := range cases {
		if got := Slug(c[0]); got != c[1] {
			t.Errorf("Slug(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	p := impTestParse(t, "magpie://import?name=302.AI&chat=https://a.example/v1")
	if p.ID != "302-ai" {
		t.Errorf("derived id for 302.AI = %q", p.ID)
	}
}

func TestImportKeyRules(t *testing.T) {
	if _, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&key=sk-a%0Ab"); err == nil {
		t.Error("key with newline accepted")
	}
	if _, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&key=sk-a%0A"); err == nil {
		t.Error("key with trailing newline accepted")
	}
	long := strings.Repeat("k", 4097)
	if _, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&key=" + long); err == nil {
		t.Error("4097-character key accepted")
	}
	p := impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1&key="+strings.Repeat("k", 4096))
	if len(p.Key) != 4096 {
		t.Errorf("4096-character key length = %d", len(p.Key))
	}
	p = impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1")
	if p.Key != "" {
		t.Errorf("missing key = %q, want empty", p.Key)
	}
}

func TestImportNameTruncation(t *testing.T) {
	long := strings.Repeat("a", 100)
	p := impTestParse(t, "magpie://import?name="+long+"&chat=https://a.example/v1")
	if len([]rune(p.Name)) != 80 {
		t.Errorf("name length = %d, want 80", len([]rune(p.Name)))
	}
}

func TestImportModels(t *testing.T) {
	p := impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1&models=%20gpt-a,,%20gpt-b%20,%20")
	if strings.Join(p.Models, "|") != "gpt-a|gpt-b" {
		t.Errorf("models = %q", p.Models)
	}
	var many []string
	for i := 0; i < 201; i++ {
		many = append(many, "model-"+strconv.Itoa(i))
	}
	if _, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&models=" + strings.Join(many, ",")); err == nil {
		t.Error("201 models accepted, want rejection")
	}
}

func TestImportWebsiteAndKeysOnlyHTTPS(t *testing.T) {
	p := impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1&website=http://a.example&keys=javascript:alert(1)")
	if p.Website != "" || p.KeysURL != "" {
		t.Errorf("non-https website/keys kept: %q %q", p.Website, p.KeysURL)
	}
	p = impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1&website=https://a.example/&keys=https://a.example/keys")
	if p.Website != "https://a.example" || p.KeysURL != "https://a.example/keys" {
		t.Errorf("https website/keys = %q %q", p.Website, p.KeysURL)
	}
}

func TestImportWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	impTestParse(t, "magpie://import?name=A&chat=https://a.example/v1&key=sk-x")
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("ParseImport wrote %s (stat err %v)", Path(), err)
	}
}
