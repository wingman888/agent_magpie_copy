package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveRejectsUnknownIconReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := Save(Provider{ID: "mine", Name: "Mine", Chat: "https://a.example/v1", Key: "k", Icon: "file:../../etc/passwd"})
	if err == nil {
		t.Fatal("a path-like icon reference should be rejected")
	}
	if err := Save(Provider{ID: "mine", Name: "Mine", Chat: "https://a.example/v1", Key: "k", Icon: "file:0123456789abcdef.png"}); err != nil {
		t.Fatalf("a well-formed (even if absent) stored name should save: %v", err)
	}
}

func TestSaveRejectsIconOnPreset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "k"
	p.Icon = "openai"
	if err := Save(p); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置图标") {
		t.Fatalf("preset icon change: err = %v, want the custom-only message", err)
	}
	p.Icon = "" // restoring the default is fine
	if err := Save(p); err != nil {
		t.Fatalf("preset with default icon should save: %v", err)
	}
}

func TestFillUnsetKeepsStoredFieldsOnlyWhereUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://a.example/v1", Key: "k1", KeyName: "personal",
		Keys:    []KeyEntry{{Name: "team", Key: "k2"}},
		Headers: map[string]string{"X-Org-Id": "acme"}, Fallback: []string{"openai/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	// a re-add that sets only the URL keeps everything else
	got := FillUnset(Provider{ID: "relay", Name: "Relay", Chat: "https://b.example/v1", Key: "k1"})
	if len(got.Keys) != 1 || got.Keys[0].Key != "k2" || got.KeyName != "personal" ||
		got.Headers["X-Org-Id"] != "acme" || len(got.Fallback) != 1 {
		t.Fatalf("unset fields not filled: %+v", got)
	}
	// values the caller gives are kept
	got = FillUnset(Provider{ID: "relay", Name: "Relay", Headers: map[string]string{"X-New": "1"}, Fallback: []string{"a/b"}})
	if _, ok := got.Headers["X-Org-Id"]; ok || got.Headers["X-New"] != "1" || got.Fallback[0] != "a/b" {
		t.Fatalf("explicit fields were overwritten: %+v", got)
	}
	// an unknown id is returned unchanged
	if got := FillUnset(Provider{ID: "new-one", Name: "New"}); got.Keys != nil || got.Headers != nil {
		t.Fatalf("unknown id should not gain fields: %+v", got)
	}
}

func TestWriteCurrentCodexRefusesUnparseableAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.json")
	orig := []byte("{ this is not json")
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrent("codex", map[string]any{"tokens": map[string]any{"access_token": "x"}}); err == nil {
		t.Fatal("writeCurrent should fail on an unparseable auth.json")
	}
	if b, _ := os.ReadFile(path); string(b) != string(orig) {
		t.Fatalf("auth.json was overwritten: %q", b)
	}
}

func TestTokenClientHasTimeout(t *testing.T) {
	if tokenClient.Timeout <= 0 {
		t.Fatal("token refreshes must have a timeout")
	}
}
