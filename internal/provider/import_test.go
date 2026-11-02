package provider

import (
	"strings"
	"testing"
)

func TestParseImportOK(t *testing.T) {
	long := strings.Repeat("n", 100)
	cases := []struct {
		link string
		chk  func(Provider) bool
	}{
		{"magpie://import?preset=deepseek&key=sk-abc&models=a,%20b", func(p Provider) bool {
			return p.ID == "deepseek" && p.Key == "sk-abc" && len(p.Models) == 2 && p.Models[1] == "b" && p.Chat != ""
		}},
		{"magpie://import?name=My%20Relay&chat=https://relay.example/v1/&anthropic=https://relay.example&website=https://relay.example&keys=javascript:alert(1)", func(p Provider) bool {
			return p.ID == "my-relay" && p.Chat == "https://relay.example/v1" && p.Anthropic == "https://relay.example" &&
				p.Website == "https://relay.example" && p.KeysURL == ""
		}},
		{"magpie://import?preset=yylx&region=cn", func(p Provider) bool { return p.Chat == "https://cn.yylx.io/v1" }},
		{"magpie://import?preset=deepseek&name=Mine&id=x1", func(p Provider) bool { return p.Name == "Mine" && p.ID == "x1" }},
		{"magpie://import?name=a&chat=http://localhost:11434/v1", func(p Provider) bool { return p.Chat == "http://localhost:11434/v1" }},
		{"magpie://import?name=a&chat=http://127.0.0.1:8080/v1", nil},
		{"magpie://import?name=a&chat=http://192.168.1.20:8000/v1", nil},
		{"magpie://import?name=a&chat=http://box.local/v1", nil},
		{"magpie:import?preset=deepseek", nil},
		{"magpie:///import?preset=deepseek", nil},
		{"MAGPIE://import/?preset=deepseek", nil},
		{"magpie://import?name=" + long + "&chat=https://a.example", func(p Provider) bool { return len(p.Name) == 80 }},
		{"magpie://import?name=a&chat=https://a.example&catalog=bad%20id", func(p Provider) bool { return p.Catalog == "" }},
		{"magpie://import?name=a&chat=https://a.example&catalog=openai", func(p Provider) bool { return p.Catalog == "openai" }},
		{"magpie://import?preset=deepseek&chat=https://evil.example/v1", func(p Provider) bool { return !strings.Contains(p.Chat, "evil") }},
	}
	for _, c := range cases {
		p, err := ParseImport(c.link)
		if err != nil {
			t.Errorf("%s: %v", c.link, err)
			continue
		}
		if c.chk != nil && !c.chk(p) {
			t.Errorf("%s: unexpected %+v", c.link, p)
		}
	}
}

func TestParseImportModelsCap(t *testing.T) {
	var ms []string
	for i := 0; i < 300; i++ {
		ms = append(ms, "m"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	p, err := ParseImport("magpie://import?preset=deepseek&models=" + strings.Join(ms, ","))
	if err != nil || len(p.Models) != 200 {
		t.Fatalf("%v %d", err, len(p.Models))
	}
}

func TestParseImportReject(t *testing.T) {
	for _, l := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?",
		"magpie://import?key=sk",
		"magpie://import?preset=nope",
		"magpie://import?preset=yylx&region=nowhere",
		"magpie://import?name=x",
		"magpie://import?name=x&chat=http://relay.example/v1",
		"magpie://import?name=x&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=x&chat=file:///etc/passwd",
		"magpie://import?name=x&chat=https://relay.example/v1%3Fa=1",
		"magpie://import?name=x&chat=https://relay.example/v1%23f",
		"magpie://import?name=x&chat=relay.example/v1",
		"magpie://import?name=x&chat=http://8.8.8.8/v1",
		"magpie://import?name=magpie&chat=https://a.example",
		"magpie://import?name=x&id=magpie&chat=https://a.example",
		"magpie://import?name=%E2%80%94&chat=https://a.example",
		"magpie://import?name=x&id=!!!&chat=https://a.example",
		"magpie://import?preset=deepseek&key=sk%0Aabc",
		"magpie://import?preset=deepseek&key=" + strings.Repeat("k", 4097),
	} {
		if p, err := ParseImport(l); err == nil {
			t.Errorf("accepted %s: %+v", l, p)
		}
	}
}
