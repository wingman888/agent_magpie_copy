package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseImportAccepts(t *testing.T) {
	for _, l := range []string{
		"magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1/",
		"magpie:import?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"magpie:///import?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"MAGPIE://Import?name=Acme%20Relay&chat=https://api.acme.example/v1",
		"magpie://import/?name=Acme%20Relay&chat=https://api.acme.example/v1",
	} {
		p, err := ParseImport(l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if p.ID != "acme-relay" || p.Name != "Acme Relay" || p.Chat != "https://api.acme.example/v1" {
			t.Fatalf("%s: %+v", l, p)
		}
	}
}

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc")
	if err != nil || p.ID != "deepseek" || p.Preset != "deepseek" || p.Chat != "https://api.deepseek.com/v1" || p.Key != "sk-abc" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = ParseImport("magpie://import?preset=deepseek&name=Mine&id=mine")
	if err != nil || p.Name != "Mine" || p.ID != "mine" {
		t.Fatalf("%+v %v", p, err)
	}
	// regions
	var withRegions *PresetDef
	for _, d := range Presets() {
		if len(d.Regions) > 1 {
			d := d
			withRegions = &d
			break
		}
	}
	if withRegions != nil {
		r := withRegions.Regions[1]
		p, err := ParseImport("magpie://import?preset=" + withRegions.ID + "&region=" + r.ID)
		if err != nil || p.Chat != r.Chat || p.Anthropic != r.Anthropic {
			t.Fatalf("region: %+v %v", p, err)
		}
	}
	if _, err := ParseImport("magpie://import?preset=deepseek&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
	if _, err := ParseImport("magpie://import?preset=nonesuch"); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestParseImportRejects(t *testing.T) {
	base := "magpie://import?name=X&chat=https://a.example/v1"
	for name, l := range map[string]string{
		"other scheme":   "https://import?name=X&chat=https://a.example/v1",
		"other action":   "magpie://delete?id=openai",
		"action prefix":  "magpie://importer?name=X&chat=https://a.example/v1",
		"no name":        "magpie://import?chat=https://a.example/v1",
		"no base":        "magpie://import?name=X",
		"http public":    "magpie://import?name=X&chat=http://a.example/v1",
		"userinfo":       "magpie://import?name=X&chat=https://u:p@a.example/v1",
		"file":           "magpie://import?name=X&chat=file:///etc/passwd",
		"query in base":  "magpie://import?name=X&chat=" + "https%3A%2F%2Fa.example%2Fv1%3Fa%3D1",
		"fragment":       "magpie://import?name=X&chat=" + "https%3A%2F%2Fa.example%2Fv1%23x",
		"id magpie":      base + "&id=magpie",
		"name magpie":    "magpie://import?name=Magpie&chat=https://a.example/v1",
		"symbol name":    "magpie://import?name=%21%21%21&chat=https://a.example/v1",
		"bad id":         base + "&id=Not_Slug",
		"key newline":    base + "&key=sk-a%0Ab",
		"key too long":   base + "&key=" + strings.Repeat("a", 4097),
		"public http v6": "magpie://import?name=X&chat=http://%5B2001:db8::1%5D/v1",
	} {
		if p, err := ParseImport(l); err == nil {
			t.Errorf("%s accepted: %+v", name, p)
		}
	}
}

func TestParseImportHTTPLocalAndFields(t *testing.T) {
	for _, h := range []string{"127.0.0.1:8080", "localhost:1234", "10.0.0.5", "192.168.1.2:9", "box.local", "[::1]:80"} {
		p, err := ParseImport("magpie://import?name=L&chat=http://" + h + "/v1")
		if err != nil || p.Chat != "http://"+h+"/v1" {
			t.Errorf("%s: %+v %v", h, p, err)
		}
	}
	p, err := ParseImport("magpie://import?name=X&anthropic=https://a.example/&responses=https://a.example/v1" +
		"&models=a,%20b,,a,c&catalog=zai&website=http://x.example&keys=https://k.example/get")
	if err != nil {
		t.Fatal(err)
	}
	if p.Anthropic != "https://a.example" || p.Responses != "https://a.example/v1" || p.Catalog != "zai" {
		t.Fatalf("%+v", p)
	}
	if strings.Join(p.Models, ",") != "a,b,c" {
		t.Fatalf("models %v", p.Models)
	}
	if p.Website != "" || p.KeysURL != "https://k.example/get" {
		t.Fatalf("urls %q %q", p.Website, p.KeysURL)
	}
	// name truncation, models cap
	long := strings.Repeat("n", 120)
	p, err = ParseImport("magpie://import?id=x&name=" + long + "&chat=https://a.example/v1")
	if err != nil || len([]rune(p.Name)) != 80 {
		t.Fatalf("name %d %v", len(p.Name), err)
	}
	var ms []string
	for i := 0; i < 300; i++ {
		ms = append(ms, "m"+string(rune('a'+i%26))+strings.Repeat("x", i/26))
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://a.example/v1&models=" + strings.Join(ms, ","))
	if err != nil || len(p.Models) != 200 {
		t.Fatalf("models %d %v", len(p.Models), err)
	}
}

func TestDescribeImportHidesKey(t *testing.T) {
	p, _ := ParseImport("magpie://import?name=L&chat=http://127.0.0.1:8080/v1&key=sk-supersecretvalue")
	out := strings.Join(DescribeImport(p), "\n")
	if strings.Contains(out, "sk-supersecretvalue") || !strings.Contains(out, "127.0.0.1") {
		t.Fatal(out)
	}
}

func TestSaveDraftWithoutKey(t *testing.T) {
	signIn(t)
	p, _ := ParseImport("magpie://import?name=Relay&chat=https://r.example/v1")
	if err := Save(p); err == nil {
		t.Fatal("Save without key should fail")
	}
	if err := SaveDraft(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil || got.Ready() {
		t.Fatalf("%+v %v", got, err)
	}
	if err := SaveDraft(Provider{ID: "codex", Name: "x", Chat: "https://r.example/v1"}); err == nil {
		t.Fatal("import over a signed-in account should fail")
	}
}

func fakeClaudeCLI(t *testing.T, script string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeStatusMu.Lock()
	claudeStatusAt, claudeStatusOut, claudeStatusUser, claudeStatusPlan = time.Time{}, false, "", ""
	claudeStatusMu.Unlock()
	t.Cleanup(func() {
		claudeStatusMu.Lock()
		claudeStatusAt, claudeStatusOut, claudeStatusUser, claudeStatusPlan = time.Time{}, false, "", ""
		claudeStatusMu.Unlock()
	})
}

func TestClaudeSignedOutStatusHidesProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	fakeClaudeCLI(t, `echo '{"loggedIn": false}'; exit 1`)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed although `claude auth status` says signed out")
	}
	// logged in: listed again
	fakeClaudeCLI(t, `echo '{"loggedIn": true, "email": "me@example.com"}'`)
	if p, ok := find(All(), "claude"); !ok || p.Account.User != "me@example.com" {
		t.Fatalf("%+v %v", p, ok)
	}
	// unparseable output keeps the credential-based judgement
	fakeClaudeCLI(t, `echo 'oops'; exit 3`)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("unparseable status should not hide claude")
	}
}
