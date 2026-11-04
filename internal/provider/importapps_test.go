package provider

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sqliteFixture(t *testing.T, path string, stmts ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func itemsOf(t *testing.T, id string) map[string]AppImport {
	t.Helper()
	for _, s := range ImportSources() {
		if s.ID == id {
			if !s.Found || s.Error != "" {
				t.Fatalf("%s: found %v, %s", id, s.Found, s.Error)
			}
			out := map[string]AppImport{}
			for _, it := range s.Items {
				out[it.Ref] = it
			}
			return out
		}
	}
	t.Fatalf("no source %s", id)
	return nil
}

// CC Switch keeps each agent's own settings; magpie reads them as the
// providers they point at, and adds only what the user picks.
func TestImportCCSwitch(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	sqliteFixture(t, filepath.Join(home, ".cc-switch", "cc-switch.db"),
		`CREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, website_url TEXT, category TEXT, created_at INTEGER, sort_index INTEGER, PRIMARY KEY (id, app_type))`,
		`INSERT INTO providers VALUES ('official','claude','Claude Official','{"env":{}}','','official',1,0)`,
		`INSERT INTO providers VALUES ('relay','claude','Some Relay','{"env":{"ANTHROPIC_BASE_URL":"https://relay.example.com","ANTHROPIC_AUTH_TOKEN":"sk-relay","ANTHROPIC_MODEL":"claude-sonnet-5"}}','https://relay.example.com','custom',2,1)`,
		`INSERT INTO providers VALUES ('relay','codex','Some Relay','{"auth":{"OPENAI_API_KEY":"sk-relay"},"config":"model_provider = \"relay\"\nmodel = \"gpt-5.5\"\n\n[model_providers.relay]\nname = \"relay\"\nbase_url = \"https://relay.example.com/v1\"\nwire_api = \"responses\"\n"}','','custom',3,0)`,
		`INSERT INTO providers VALUES ('ds','claude','DeepSeek','{"env":{"ANTHROPIC_BASE_URL":"https://api.deepseek.com/anthropic","ANTHROPIC_AUTH_TOKEN":"sk-ds"}}','','cn_official',4,2)`,
		`INSERT INTO providers VALUES ('dial','pi','dial','{"api":"openai-completions","apiKey":"magpie","baseUrl":"http://127.0.0.1:3425/v1"}','','custom',5,0)`,
		`INSERT INTO providers VALUES ('g','gemini','Gemini relay','{"env":{"GOOGLE_GEMINI_BASE_URL":"https://g.example.com","GEMINI_API_KEY":"k"}}','','custom',6,0)`,
	)
	// DeepSeek is already here under its preset id, with another key
	if err := Save(FromPresetKey(t, "deepseek", "sk-other")); err != nil {
		t.Fatal(err)
	}

	items := itemsOf(t, "cc-switch")
	if it := items["claude/official"]; it.Skip == "" {
		t.Fatalf("official sign-in offered: %+v", it)
	}
	if it := items["pi/dial"]; it.Skip == "" {
		t.Fatalf("magpie itself offered: %+v", it)
	}
	if it := items["gemini/g"]; it.Skip == "" {
		t.Fatalf("gemini relay offered: %+v", it)
	}
	relay := items["claude/relay"]
	if relay.Skip != "" || relay.Status != "new" || relay.Provider.Anthropic != "https://relay.example.com" || relay.Provider.Responses != "https://relay.example.com/v1" || relay.From != "Claude Code, Codex" {
		t.Fatalf("relay: %+v", relay)
	}
	if _, ok := items["codex/relay"]; ok {
		t.Fatal("the Codex half of the relay listed on its own")
	}
	ds := items["claude/ds"]
	if ds.Status != "taken" || ds.Provider.ID != "deepseek" || ds.Provider.Preset != "deepseek" || ds.KeyOf != "deepseek" {
		t.Fatalf("deepseek: %+v", ds)
	}

	added, err := ImportFromApps([]AppPick{{Source: "cc-switch", Ref: "claude/relay"}, {Source: "cc-switch", Ref: "claude/ds", Mode: "key"}})
	if err != nil || len(added) != 2 {
		t.Fatalf("import: %v %v", added, err)
	}
	if p, err := Find("some-relay"); err != nil || p.Key != "sk-relay" || len(p.Models) != 2 {
		t.Fatalf("relay saved: %+v", p)
	}
	if p, _ := Find("deepseek"); p.Key != "sk-other" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-ds" {
		t.Fatalf("deepseek's second key: %+v", p)
	}
	if it := itemsOf(t, "cc-switch")["claude/ds"]; it.Status != "same" {
		t.Fatalf("second key again: %+v", it)
	}
	// imported once, it is there already
	if it := itemsOf(t, "cc-switch")["claude/relay"]; it.Status != "same" {
		t.Fatalf("after import: %+v", it)
	}
}

func TestImportAlma(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfg, _ := os.UserConfigDir()
	sqliteFixture(t, filepath.Join(cfg, "alma", "chat_threads.db"),
		`CREATE TABLE providers (id TEXT PRIMARY KEY, name TEXT, type TEXT, api_key TEXT, models TEXT, base_url TEXT, enabled INTEGER, created_at TEXT, api_format TEXT, is_response_api INTEGER, custom_headers TEXT)`,
		`INSERT INTO providers VALUES ('a1','OpenRouter','openrouter','sk-or','["anthropic/claude-sonnet-5"]',NULL,1,'1',NULL,0,NULL)`,
		`INSERT INTO providers VALUES ('a2','My Proxy','custom','sk-p','[]','https://proxy.example.com/v1',0,'2','openai-chat',0,'{"X-Team":"a"}')`,
		`INSERT INTO providers VALUES ('a3','Copilot','copilot','','[]',NULL,1,'3',NULL,0,NULL)`,
		`INSERT INTO providers VALUES ('a4','junk','custom','x','[]','bbb',0,'4',NULL,0,NULL)`,
	)
	items := itemsOf(t, "alma")
	if it := items["a1"]; it.Provider.Preset != "openrouter" || it.Provider.Key != "sk-or" || len(it.Provider.Models) != 1 {
		t.Fatalf("openrouter: %+v", it)
	}
	if it := items["a2"]; it.Off == "" || it.Provider.Chat != "https://proxy.example.com/v1" || it.Provider.Headers["X-Team"] != "a" {
		t.Fatalf("proxy: %+v", it)
	}
	if items["a3"].Skip == "" || items["a4"].Skip == "" {
		t.Fatalf("sign-in or junk offered: %+v %+v", items["a3"], items["a4"])
	}
}

func FromPresetKey(t *testing.T, id, key string) Provider {
	p, err := FromPreset(id)
	if err != nil {
		t.Fatalf("no preset %s", id)
	}
	p.Key = key
	return p
}

func TestImportAppsByName(t *testing.T) {
	for i := 1; i < len(appReaders); i++ {
		if strings.ToLower(appReaders[i-1].name) > strings.ToLower(appReaders[i].name) {
			t.Fatalf("%s listed before %s", appReaders[i-1].name, appReaders[i].name)
		}
	}
}

// An app with nothing to bring over lists no items, not null: the window
// reads each source's items.
func TestImportSourcesNeverNull(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model": "opus"}`), 0o644)
	for _, s := range ImportSources() {
		b, _ := json.Marshal(s)
		if s.Items == nil || strings.Contains(string(b), `"items":null`) {
			t.Fatalf("%s: items is null: %s", s.ID, b)
		}
	}
}
