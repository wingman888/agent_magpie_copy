package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateConfig points the providers file at a temp dir so nothing real is touched.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestParseImportAccepts(t *testing.T) {
	isolateConfig(t)
	cases := []struct {
		name, link string
		check      func(t *testing.T, p Provider)
	}{
		{"preset", "magpie://import?preset=deepseek&key=sk-test", func(t *testing.T, p Provider) {
			if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-test" || p.Chat != "https://api.deepseek.com/v1" || p.Name != "DeepSeek" {
				t.Fatalf("%+v", p)
			}
		}},
		{"preset region", "magpie://import?preset=yylx&region=cn&key=k", func(t *testing.T, p Provider) {
			if p.Chat != "https://cn.yylx.io/v1" || p.Anthropic != "https://cn.yylx.io" {
				t.Fatalf("%+v", p)
			}
		}},
		{"preset default region", "magpie://import?preset=yylx&key=k", func(t *testing.T, p Provider) {
			if p.Chat != "https://app.yylx.io/v1" {
				t.Fatalf("%+v", p)
			}
		}},
		{"preset name and id override", "magpie://import?preset=openrouter&name=My%20OR&id=my-or", func(t *testing.T, p Provider) {
			if p.ID != "my-or" || p.Name != "My OR" || p.Preset != "openrouter" {
				t.Fatalf("%+v", p)
			}
		}},
		{"opencode go", "magpie://import?preset=opencode-go&key=k", func(t *testing.T, p Provider) {
			if p.Chat != "https://opencode.ai/zen/go/v1" || p.Anthropic != "https://opencode.ai/zen/go" {
				t.Fatalf("%+v", p)
			}
		}},
		{"custom", "magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-x", func(t *testing.T, p Provider) {
			if p.ID != "acme-relay" || p.Name != "Acme Relay" || p.Chat != "https://api.acme.example/v1" || p.Key != "sk-x" || p.Preset != "" {
				t.Fatalf("%+v", p)
			}
		}},
		{"scheme magpie:import", "magpie:import?name=A&chat=https://a.example/v1", func(t *testing.T, p Provider) {
			if p.ID != "a" {
				t.Fatalf("%+v", p)
			}
		}},
		{"scheme magpie:///import", "magpie:///import?name=A&chat=https://a.example/v1", func(t *testing.T, p Provider) {}},
		{"scheme upper case", "MAGPIE://import?name=A&chat=https://a.example/v1", func(t *testing.T, p Provider) {}},
		{"action with slash", "magpie://import/?name=A&chat=https://a.example/v1", func(t *testing.T, p Provider) {}},
		{"three bases", "magpie://import?name=R&chat=https://a.example/v1&responses=https://a.example/v1&anthropic=https://a.example", func(t *testing.T, p Provider) {
			if p.Responses == "" || p.Anthropic != "https://a.example" {
				t.Fatalf("%+v", p)
			}
		}},
		{"trailing slash trimmed", "magpie://import?name=A&chat=https://a.example/v1/", func(t *testing.T, p Provider) {
			if p.Chat != "https://a.example/v1" {
				t.Fatalf("%q", p.Chat)
			}
		}},
		{"local http loopback", "magpie://import?name=L&chat=http://127.0.0.1:8000/v1", func(t *testing.T, p Provider) {}},
		{"local http localhost", "magpie://import?name=L&chat=http://localhost:1234/v1", func(t *testing.T, p Provider) {}},
		{"local http private", "magpie://import?name=L&chat=http://192.168.1.5/v1", func(t *testing.T, p Provider) {}},
		{"local http ten net", "magpie://import?name=L&chat=http://10.0.0.2/v1", func(t *testing.T, p Provider) {}},
		{"local http .local", "magpie://import?name=L&chat=http://box.local:9000/v1", func(t *testing.T, p Provider) {}},
		{"models split and trimmed", "magpie://import?name=M&chat=https://a.example/v1&models=%20a%2C%2C%20b%20,+c%20", func(t *testing.T, p Provider) {
			if strings.Join(p.Models, "|") != "a|b|c" {
				t.Fatalf("%q", p.Models)
			}
		}},
		{"catalog", "magpie://import?name=C&chat=https://a.example/v1&catalog=deepseek", func(t *testing.T, p Provider) {
			if p.Catalog != "deepseek" {
				t.Fatalf("%+v", p)
			}
		}},
		{"website and keys https", "magpie://import?name=W&chat=https://a.example/v1&website=https://a.example&keys=https://a.example/keys", func(t *testing.T, p Provider) {
			if p.Website != "https://a.example" || p.KeysURL != "https://a.example/keys" {
				t.Fatalf("%+v", p)
			}
		}},
		{"website and keys not https are ignored", "magpie://import?name=W&chat=https://a.example/v1&website=http://a.example&keys=ftp://x", func(t *testing.T, p Provider) {
			if p.Website != "" || p.KeysURL != "" {
				t.Fatalf("%+v", p)
			}
		}},
		{"no key is fine", "magpie://import?name=N&chat=https://a.example/v1", func(t *testing.T, p Provider) {
			if p.Key != "" {
				t.Fatalf("%+v", p)
			}
		}},
		{"explicit id", "magpie://import?name=Anything&id=my-id&chat=https://a.example/v1", func(t *testing.T, p Provider) {
			if p.ID != "my-id" {
				t.Fatalf("%+v", p)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParseImport(c.link)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			c.check(t, p)
		})
	}
}

func TestParseImportTruncatesLongName(t *testing.T) {
	isolateConfig(t)
	p, err := ParseImport("magpie://import?name=" + strings.Repeat("a", 100) + "&chat=https://a.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(p.Name)) != 80 {
		t.Fatalf("name length %d", len([]rune(p.Name)))
	}
}

func TestParseImportRejects(t *testing.T) {
	isolateConfig(t)
	many := make([]string, importMaxModels+1)
	for i := range many {
		many[i] = fmt.Sprintf("m%d", i) // distinct, so none is dropped as a duplicate
	}
	long := strings.Repeat("k", importMaxKey+1)
	cases := map[string]string{
		"not magpie":              "https://example.com/import?name=x&chat=https://a.example",
		"other scheme":            "file://import?name=x&chat=https://a.example",
		"delete action":           "magpie://delete?name=x&chat=https://a.example/v1",
		"other action":            "magpie://remove?name=x&chat=https://a.example/v1",
		"no params":               "magpie://import",
		"no name without preset":  "magpie://import?chat=https://a.example/v1",
		"no base":                 "magpie://import?name=x",
		"public http":             "magpie://import?name=x&chat=http://api.example.com/v1",
		"public http anthropic":   "magpie://import?name=x&anthropic=http://api.example.com",
		"credentials":             "magpie://import?name=x&chat=https://user:pass@a.example/v1",
		"file scheme":             "magpie://import?name=x&chat=file:///etc/passwd",
		"query in base":           "magpie://import?name=x&chat=https://a.example/v1%3Fq%3D1",
		"fragment in base":        "magpie://import?name=x&chat=https://a.example/v1%23frag",
		"fragment in link":        "magpie://import?name=x&chat=https://a.example/v1#frag",
		"id magpie":               "magpie://import?name=magpie&chat=https://a.example/v1",
		"id magpie explicit":      "magpie://import?name=x&id=magpie&chat=https://a.example/v1",
		"name without characters": "magpie://import?name=%21%21%21&chat=https://a.example/v1",
		"key with newline":        "magpie://import?name=x&chat=https://a.example/v1&key=sk-secret%0A",
		"key too long":            "magpie://import?name=x&chat=https://a.example/v1&key=" + long,
		"unknown preset":          "magpie://import?preset=nosuch&key=k",
		"unknown region":          "magpie://import?preset=yylx&region=mars&key=k",
		"region without regions":  "magpie://import?preset=deepseek&region=cn&key=k",
		"region without preset":   "magpie://import?name=x&chat=https://a.example/v1&region=cn",
		"id not a slug":           "magpie://import?name=x&id=Bad_ID&chat=https://a.example/v1",
		"too many models":         "magpie://import?name=x&chat=https://a.example/v1&models=" + strings.Join(many, ","),
		"duplicate parameter":     "magpie://import?name=x&name=y&chat=https://a.example/v1",
		"base without host":       "magpie://import?name=x&chat=https://",
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := ParseImport(link)
			if err == nil {
				t.Fatalf("accepted: %+v", p)
			}
			if strings.Contains(err.Error(), "sk-secret") {
				t.Fatalf("error repeats the key: %v", err)
			}
		})
	}
}

// ParseImport never saves anything, and a rejected link saves nothing either.
func TestParseImportWritesNothing(t *testing.T) {
	isolateConfig(t)
	if _, err := ParseImport("magpie://import?name=x&chat=https://a.example/v1&key=sk-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseImport("magpie://import?name=x&chat=http://api.example.com/v1"); err == nil {
		t.Fatal("public http accepted")
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatalf("a providers file was written: %v", err)
	}
}

func TestSlugDerivesIDs(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Relay":       "acme-relay",
		"My Relay":         "my-relay",
		"  Acme   Relay  ": "acme-relay",
		"!!!":              "",
		"鱼鱼连线":             "",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	isolateConfig(t)
	p, err := ParseImport("magpie://import?name=My%20Relay&chat=https://a.example/v1")
	if err != nil || p.ID != "my-relay" {
		t.Fatalf("id: %+v %v", p, err)
	}
}

func TestSaveImportedAcceptsKeyless(t *testing.T) {
	isolateConfig(t)
	p, err := ParseImport("magpie://import?name=Nokey&chat=https://a.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveImported(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("nokey")
	if err != nil || saved.Ready() {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	// plain Save still asks for a key, as before
	if err := Save(Provider{Name: "Other", Chat: "https://a.example/v1"}); err == nil {
		t.Fatal("Save accepted a keyless provider")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "providers.json")); err != nil {
		t.Fatal(err)
	}
}
