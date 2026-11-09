package provider

import (
	"os"
	"strings"
	"testing"
)

func TestS2ImportAcceptsCustomLink(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Acme Relay" || p.ID != "acme-relay" {
		t.Errorf("name/id = %q/%q", p.Name, p.ID)
	}
	if p.Chat != "https://api.acme.example/v1" || p.Key != "sk-test" || p.Preset != "" {
		t.Errorf("provider = %+v", p)
	}
}

func TestS2ImportSchemeVariants(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=A&chat=https://a.example/v1",
		"magpie:import?name=A&chat=https://a.example/v1",
		"magpie:///import?name=A&chat=https://a.example/v1",
		"MAGPIE://import?name=A&chat=https://a.example/v1",
		"Magpie:Import?name=A&chat=https://a.example/v1",
		"magpie://import/?name=A&chat=https://a.example/v1",
		"magpie:///import/?name=A&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(link); err != nil {
			t.Errorf("%s: rejected: %v", link, err)
		}
	}
	for _, link := range []string{
		"magpie://delete?name=A&chat=https://a.example/v1",
		"magpie:delete?name=A&chat=https://a.example/v1",
		"https://import?name=A&chat=https://a.example/v1",
		"file://import?name=A&chat=https://a.example/v1",
		"magpie://import/extra?name=A&chat=https://a.example/v1",
		"magpie://?name=A&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("%s: accepted", link)
		}
	}
}

func TestS2ImportPresetAndRegion(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-x")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Chat != "https://api.deepseek.com/v1" || p.Key != "sk-x" {
		t.Errorf("preset import = %+v", p)
	}
	p, err = ParseImport("magpie://import?preset=deepseek&name=My%20DS")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "My DS" || p.ID != "deepseek" {
		t.Errorf("name override = %q/%q", p.Name, p.ID)
	}
	p, err = ParseImport("magpie://import?preset=yylx&region=global&key=sk-x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://global.yylx.io/v1" || p.Anthropic != "https://global.yylx.io" {
		t.Errorf("region endpoints = %q / %q", p.Chat, p.Anthropic)
	}
	if _, err := ParseImport("magpie://import?preset=yylx&region=mars"); err == nil {
		t.Error("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=cn"); err == nil {
		t.Error("region on a preset without regions accepted")
	}
	if _, err := ParseImport("magpie://import?preset=no-such-vendor&key=x"); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestS2ImportRejections(t *testing.T) {
	for _, link := range []string{
		"magpie://import?chat=https://a.example/v1",                   // no name without a preset
		"magpie://import?name=A",                                      // no base URL
		"magpie://import?name=A&chat=http://api.example.com/v1",       // plain http, public host
		"magpie://import?name=A&chat=https://u:p@a.example/v1",        // credentials
		"magpie://import?name=A&chat=https://a.example/v1?x=1",        // query
		"magpie://import?name=A&chat=https://a.example/v1#frag",       // fragment
		"magpie://import?name=A&chat=file:///etc/passwd",              // other scheme
		"magpie://import?name=A&chat=a.example/v1",                    // no scheme
		"magpie://import?name=magpie&chat=https://a.example/v1",       // id magpie
		"magpie://import?name=A&id=magpie&chat=https://a.example/v1",  // explicit id magpie
		"magpie://import?name=!!!&chat=https://a.example/v1",          // nothing usable in name
		"magpie://import?name=A&chat=https://a.example/v1&key=sk%0Ax", // newline in key
		"magpie://import?name=A&chat=https://a.example/v1&key=sk%0Dx", // carriage return in key
		"magpie://import?name=A&chat=https://a.example/v1&key=" + strings.Repeat("k", 4097),
		"magpie://import?name=A&chat=https://a.example/v1&id=OpenAI", // id not its own slug
		"magpie://import?name=A&chat=https://a.example/v1&id=my%20relay",
		"magpie://import?name=A&chat=https://a.example/v1&id=-x-",
		"magpie://import?name=A&chat=https://a.example/v1&models=" + strings.Repeat("m,", 200) + "m",
		"magpie://import?region=global&name=A&chat=https://a.example/v1",
	} {
		if _, err := ParseImport(link); err == nil {
			t.Errorf("accepted: %s", link)
		}
	}
}

func TestS2ImportLocalHTTPAllowed(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:8080/v1",
		"http://localhost:11434/v1",
		"http://192.168.1.5/v1",
		"http://10.0.0.7:3000/v1",
		"http://relay.local/v1",
		"http://[::1]:9000/v1",
	} {
		if _, err := ParseImport("magpie://import?name=L&chat=" + base); err != nil {
			t.Errorf("%s: rejected: %v", base, err)
		}
	}
}

func TestS2ImportBaseTrailingSlash(t *testing.T) {
	p, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1/&anthropic=https://a.example/")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://a.example/v1" || p.Anthropic != "https://a.example" {
		t.Errorf("bases = %q / %q", p.Chat, p.Anthropic)
	}
}

func TestS2ImportWebsiteKeysOnlyHTTPS(t *testing.T) {
	p, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&website=http://a.example&keys=https://a.example/keys")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "" {
		t.Errorf("non-https website kept: %q", p.Website)
	}
	if p.KeysURL != "https://a.example/keys" {
		t.Errorf("keys = %q", p.KeysURL)
	}
	p, err = ParseImport("magpie://import?name=A&chat=https://a.example/v1&website=https://a.example&keys=file:///x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "https://a.example" || p.KeysURL != "" {
		t.Errorf("website/keys = %q / %q", p.Website, p.KeysURL)
	}
	// a preset keeps its own website when the link's is not https
	p, err = ParseImport("magpie://import?preset=deepseek&website=ftp://x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Website != "https://platform.deepseek.com" {
		t.Errorf("preset website replaced: %q", p.Website)
	}
}

func TestS2ImportIDDerivation(t *testing.T) {
	cases := map[string]string{
		"name=Acme%20Relay":         "acme-relay",
		"name=My%20Relay":           "my-relay",
		"name=My%20-%20Relay":       "my-relay",
		"name=Relay%20%F0%9F%9A%80": "relay",
	}
	for q, want := range cases {
		p, err := ParseImport("magpie://import?" + q + "&chat=https://a.example/v1")
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if p.ID != want {
			t.Errorf("%s: id %q, want %q", q, p.ID, want)
		}
	}
	p, err := ParseImport("magpie://import?name=Whatever&id=my-relay&chat=https://a.example/v1")
	if err != nil || p.ID != "my-relay" {
		t.Errorf("explicit id: %q, %v", p.ID, err)
	}
}

func TestS2ImportNameTruncated(t *testing.T) {
	long := strings.Repeat("x", 90)
	p, err := ParseImport("magpie://import?name=" + long + "&chat=https://a.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Name)); n != 80 {
		t.Errorf("name has %d runes, want 80", n)
	}
}

func TestS2ImportModels(t *testing.T) {
	p, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&models=" + "a%2C%20%2C%20b%2C%2C+c%20")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Models, "|") != "a|b|c" {
		t.Errorf("models = %q", p.Models)
	}
	many := strings.Repeat("m,", 199) + "m"
	if _, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&models=" + many); err != nil {
		t.Errorf("200 models rejected: %v", err)
	}
}

func TestS2ImportDoesNotSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if _, err := ParseImport("magpie://import?name=Quiet&chat=https://q.example/v1&key=sk-quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("ParseImport wrote %s (stat err %v)", Path(), err)
	}
}

func TestS2ImportErrorsDoNotEchoKey(t *testing.T) {
	_, err := ParseImport("magpie://import?name=A&chat=https://a.example/v1&key=sk-secret%zz")
	if err == nil {
		t.Fatal("malformed query accepted")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("error echoes the key: %v", err)
	}
	_, err = ParseImport("magpie://import?name=A&chat=https://u:sk-secret@a.example/v1")
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("credentials error echoes the link: %v", err)
	}
}
