package provider

import (
	"os"
	"strings"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc&models=a,%20b")
	if err != nil {
		t.Fatal(err)
	}
	pr := Preset("deepseek")
	if p.ID != "deepseek" || p.Key != "sk-abc" || p.Preset != "deepseek" {
		t.Fatalf("got id=%q key=%q preset=%q", p.ID, p.Key, p.Preset)
	}
	if p.Chat != pr.Chat || p.Anthropic != pr.Anthropic || p.Responses != pr.Responses {
		t.Fatalf("endpoints not from preset: %+v", p)
	}
	if strings.Join(p.Models, "|") != "a|b" {
		t.Fatalf("models = %q, want a,b", p.Models)
	}
	if p.Name != pr.Name || p.Catalog != pr.Catalog || p.Website != pr.Website {
		t.Fatalf("preset metadata lost: %+v", p)
	}
}

func TestParseImportPresetNoKeyAndOverrides(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&name=Work%20DS&id=ds2&chat=https://evil.example/v1&keys=https://x.example/keys")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "" {
		t.Fatalf("key = %q, want empty", p.Key)
	}
	if p.Name != "Work DS" || p.ID != "ds2" {
		t.Fatalf("name=%q id=%q", p.Name, p.ID)
	}
	if p.Chat != Preset("deepseek").Chat {
		t.Fatalf("chat from link applied to preset link: %q", p.Chat)
	}
	if p.KeysURL != Preset("deepseek").KeysURL {
		t.Fatalf("keys from link applied to preset link: %q", p.KeysURL)
	}
}

func TestParseImportPresetDefaultID(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=openai&name=Mine")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "openai" || p.Name != "Mine" {
		t.Fatalf("id=%q name=%q", p.ID, p.Name)
	}
}

func TestParseImportCustom(t *testing.T) {
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)&key=sk-1&models=gpt-5.5,%20claude-sonnet-5,")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" {
		t.Fatalf("id=%q name=%q", p.ID, p.Name)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Fatalf("chat = %q, want trailing slash removed", p.Chat)
	}
	if p.Anthropic != "https://relay.example" {
		t.Fatalf("anthropic = %q", p.Anthropic)
	}
	if p.Website != "https://relay.example" {
		t.Fatalf("website = %q", p.Website)
	}
	if p.KeysURL != "" {
		t.Fatalf("javascript keys URL kept: %q", p.KeysURL)
	}
	if p.Preset != "" || p.Key != "sk-1" {
		t.Fatalf("preset=%q key=%q", p.Preset, p.Key)
	}
	if strings.Join(p.Models, "|") != "gpt-5.5|claude-sonnet-5" {
		t.Fatalf("models = %q", p.Models)
	}
}

func TestParseImportCatalog(t *testing.T) {
	p, err := ParseImport("magpie://import?name=R&chat=https://r.example/v1&catalog=OpenAI")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai" {
		t.Fatalf("catalog = %q", p.Catalog)
	}
	p, err = ParseImport("magpie://import?name=R&chat=https://r.example/v1&catalog=bad%20id")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "" {
		t.Fatalf("invalid catalog kept: %q", p.Catalog)
	}
}

func TestParseImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"  magpie://import?preset=deepseek  ",
	} {
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%q: %v", link, err)
			continue
		}
		if p.ID != "deepseek" {
			t.Errorf("%q: id = %q", link, p.ID)
		}
	}
}

func TestParseImportRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=yylx&region=cn")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
		t.Fatalf("region endpoints not used: chat=%q anthropic=%q", p.Chat, p.Anthropic)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Fatal("region on a preset without regions accepted")
	}
}

func TestParseImportRejects(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?",
		"magpie://import?preset=nope",
		"magpie://import?chat=",
		"magpie://import?name=Relay",
		"magpie://import?name=Relay&chat=http://relay.example/v1",
		"magpie://import?name=Relay&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=Relay&chat=file:///etc/passwd",
		"magpie://import?name=Relay&chat=https://relay.example/v1?x=1",
		"magpie://import?name=Relay&chat=https://relay.example/v1%23frag",
		"magpie://import?name=magpie&chat=https://relay.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://relay.example/v1",
		"magpie://import?preset=deepseek&key=sk-abc%0A",
		"magpie://import?preset=deepseek&key=sk-abc%0Dx",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097),
		"magpie://import?preset=deepseek&id=magpie",
		"magpie://import?preset=deepseek&id=%E2%80%94",
		"magpie://import?name=Relay&chat=https://relay.example/v1&id=--",
		"magpie://import?name=Relay%0A&chat=https://relay.example/v1",
		"magpie://import?name=Relay&chat=%zz",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("accepted %q", link)
		}
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://127.0.0.1:8080/v1",
		"http://192.168.1.20:8000/v1",
		"http://10.0.0.5/v1",
		"http://172.16.4.4/v1",
		"http://[::1]:8000/v1",
		"http://box.local/v1",
	} {
		link := "magpie://import?name=Local&chat=" + base
		p, err := ParseImport(link)
		if err != nil {
			t.Errorf("%s: %v", base, err)
			continue
		}
		if p.Chat != base {
			t.Errorf("chat = %q, want %q", p.Chat, base)
		}
	}
}

func TestParseImportNameTruncated(t *testing.T) {
	long := strings.Repeat("界", 100)
	p, err := ParseImport("magpie://import?name=" + long + "&chat=https://r.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Name)); n != 80 {
		t.Fatalf("name has %d runes, want 80", n)
	}
}

func TestParseImportModelsCapped(t *testing.T) {
	var ms []string
	for i := 0; i < 250; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	p, err := ParseImport("magpie://import?preset=deepseek&models=" + strings.Join(ms, ","))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 200 {
		t.Fatalf("%d models, want exactly 200", len(p.Models))
	}
}

func TestParseImportDoesNotSave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseImport("magpie://import?name=R&chat=https://r.example/v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatalf("parsing wrote %s (stat err: %v)", Path(), err)
	}
}
