package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func home(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "DSH_HOME", "GEMINI_CLI_HOME", "OPENCODE_CONFIG"} {
		t.Setenv(v, "")
	}
}

func setUp(t *testing.T) {
	t.Helper()
	icon, err := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\n0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "acme", Name: "Acme", Chat: "https://acme.example.com/v1", Key: "sk-acme", Icon: icon,
			Keys:    []provider.KeyAccount{{Name: "second", Key: "sk-acme-2"}},
			Headers: map[string]string{"X-Team": "a", "X-Api-Key": "hdr-secret"}},
		{ID: "beta", Name: "Beta", Chat: "https://beta.example.com/v1", Key: "sk-beta"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work", profile.Profile{"claude.model": "acme/m1"}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptRoundTrip(t *testing.T) {
	home(t)
	setUp(t)
	b, err := backup.Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Keys || b.App != "test" || len(b.Icons) != 1 {
		t.Fatalf("bundle: keys=%v app=%q icons=%d", b.Keys, b.App, len(b.Icons))
	}
	data, err := backup.Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-acme", "sk-acme-2", "hdr-secret", "acme.example.com", "work", "Acme"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("%q readable in the file", secret)
		}
	}
	if _, err := backup.Open(data, "wrong"); !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}

	home(t)
	got, err := backup.Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	r, err := backup.Restore(got, backup.All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || !r.Settings || r.Profiles != 1 || len(r.NeedKey) != 0 {
		t.Fatalf("result: %+v", r)
	}
	p, err := provider.Find("acme")
	if err != nil || p.Key != "sk-acme" || len(p.Keys) != 1 || p.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatalf("acme: %+v %v", p, err)
	}
	name, _ := strings.CutPrefix(p.Icon, "file:")
	if _, err := os.Stat(provider.IconFile(name)); err != nil {
		t.Fatalf("icon: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("settings: %+v", s)
	}
	if ps, _ := profile.Load(); ps["work"]["claude.model"] != "acme/m1" {
		t.Fatalf("profiles: %+v", ps)
	}
}

func TestAcceptNoKeys(t *testing.T) {
	home(t)
	setUp(t)
	b, err := backup.Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Keys {
		t.Fatal("Keys true in keyless backup")
	}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) != 0 {
			t.Fatalf("key in a keyless backup: %+v", p)
		}
		if _, ok := p.Headers["X-Api-Key"]; ok {
			t.Fatalf("auth header kept: %+v", p.Headers)
		}
		if p.ID == "acme" && p.Headers["X-Team"] != "a" {
			t.Fatalf("X-Team lost: %+v", p.Headers)
		}
	}
	data, _ := backup.Seal(b, "pw")

	home(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme old", Chat: "https://old.example.com/v1", Key: "sk-here"}); err != nil {
		t.Fatal(err)
	}
	got, err := backup.Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	r, err := backup.Restore(got, backup.Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 1 || !slices.Equal(r.NeedKey, []string{"Beta"}) || r.Settings || r.Profiles != 0 {
		t.Fatalf("result: %+v", r)
	}
	if p, _ := provider.Find("acme"); p.Key != "sk-here" || p.Chat != "https://acme.example.com/v1" || p.Headers["X-Team"] != "a" {
		t.Fatalf("acme: %+v", p)
	}
}

func TestAcceptNotABackup(t *testing.T) {
	for _, data := range []string{"", "{}", `{"format":"magpie-backup","version":9,"kdf":"x"}`} {
		_, err := backup.Open([]byte(data), "pw")
		if err == nil || errors.Is(err, backup.ErrPassphrase) {
			t.Fatalf("%q: %v", data, err)
		}
		if strings.Contains(data, "version") && !strings.Contains(err.Error(), "newer magpie") {
			t.Fatalf("%q: %v", data, err)
		}
	}
	if _, err := backup.Seal(backup.Bundle{}, ""); err == nil {
		t.Fatal("sealed with no passphrase")
	}
}

func TestAcceptTampered(t *testing.T) {
	data, err := backup.Seal(backup.Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Open(data, "pw"); err != nil {
		t.Fatalf("clean open: %v", err)
	}
	bad := strings.Replace(string(data), `"iterations": 600000`, `"iterations": 100000`, 1)
	if bad == string(data) {
		t.Skip("envelope not formatted as expected")
	}
	if _, err := backup.Open([]byte(bad), "pw"); !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("tampered header: %v", err)
	}
}

func TestAcceptSealRandomised(t *testing.T) {
	a, _ := backup.Seal(backup.Bundle{Version: 1}, "pw")
	b, _ := backup.Seal(backup.Bundle{Version: 1}, "pw")
	if string(a) == string(b) {
		t.Fatal("two seals identical: salt/nonce not random")
	}
}

func TestAcceptAgentsSkippedWithoutAgents(t *testing.T) {
	home(t)
	b := backup.Bundle{Version: 1, Agents: profile.Profile{"codex.model": "x", "claude.model": "y", "codex.provider": "z"}}
	r, err := backup.Restore(b, backup.All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 0 {
		t.Fatalf("agents: %+v", r)
	}
	for k := range b.Agents {
		if !slices.Contains(r.Skipped, k) {
			t.Fatalf("%s not skipped: %v", k, r.Skipped)
		}
	}
}

func TestAcceptRestoreIgnoresBadIDs(t *testing.T) {
	home(t)
	b := backup.Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "M", Chat: "https://m.example.com/v1", Key: "k"},
		{ID: "", Name: "E", Chat: "https://e.example.com/v1", Key: "k"},
		{ID: "Not A Slug!", Name: "S", Chat: "https://s.example.com/v1", Key: "k"},
		{ID: "good", Name: "Good", Chat: "https://g.example.com/v1", Key: "k"},
	}}
	r, err := backup.Restore(b, backup.Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 {
		t.Fatalf("result: %+v", r)
	}
	if _, err := provider.Find("good"); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptProviderRestoreKeepsLocalKeys(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Old", Chat: "https://old.example.com/v1", Key: "sk-here",
		Keys: []provider.KeyAccount{{Name: "n", Key: "sk-2"}}}); err != nil {
		t.Fatal(err)
	}
	added, replaced, err := provider.Restore([]provider.Provider{{ID: "acme", Name: "New", Chat: "https://new.example.com/v1"}})
	if err != nil || added != 0 || replaced != 1 {
		t.Fatalf("%d %d %v", added, replaced, err)
	}
	p, _ := provider.Find("acme")
	if p.Key != "sk-here" || len(p.Keys) != 1 || p.Chat != "https://new.example.com/v1" {
		t.Fatalf("%+v", p)
	}
	n := len(provider.Stored())
	if a, r, err := provider.Restore([]provider.Provider{{ID: "magpie"}}); err != nil || a != 0 || r != 0 || len(provider.Stored()) != n {
		t.Fatalf("%d %d %v", a, r, err)
	}
}
