package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func machine(t *testing.T) {
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

func populate(t *testing.T) {
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
	if err := profile.Save("workprofile", profile.Profile{"claude.model": "acme/m1"}); err != nil {
		t.Fatal(err)
	}
}

func sealed(t *testing.T, keys bool, pass string) []byte {
	t.Helper()
	b, err := backup.Collect(keys, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := backup.Seal(b, pass)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAcceptRoundTripAndOpaque(t *testing.T) {
	machine(t)
	populate(t)
	data := sealed(t, true, "correct horse")
	for _, s := range []string{"sk-acme", "sk-beta", "hdr-secret", "acme.example.com", "workprofile", "X-Team"} {
		if strings.Contains(string(data), s) {
			t.Fatalf("%q readable in file", s)
		}
	}
	machine(t)
	b, err := backup.Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	r, err := backup.Restore(b, backup.All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Replaced != 0 || !r.Settings || r.Profiles != 1 || len(r.NeedKey) != 0 {
		t.Fatalf("result %+v", r)
	}
	p, err := provider.Find("acme")
	if err != nil || p.Key != "sk-acme" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-acme-2" || p.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatalf("acme %+v %v", p, err)
	}
	name, _ := strings.CutPrefix(p.Icon, "file:")
	if data, err := os.ReadFile(provider.IconFile(name)); err != nil || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("icon %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("settings %+v", s)
	}
	if ps, _ := profile.Load(); ps["workprofile"]["claude.model"] != "acme/m1" {
		t.Fatalf("profiles %+v", ps)
	}
	// restoring again replaces
	r, err = backup.Restore(b, backup.All)
	if err != nil || r.Added != 0 || r.Replaced != 2 {
		t.Fatalf("again %+v %v", r, err)
	}
}

func TestAcceptWrongPassphraseAndTamper(t *testing.T) {
	machine(t)
	populate(t)
	data := sealed(t, true, "pw")
	if _, err := backup.Open(data, "nope"); !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("wrong pass: %v", err)
	}
	re := regexp.MustCompile(`"iterations":\s*\d+`)
	if !re.Match(data) {
		t.Fatal("no iterations in header")
	}
	bad := re.ReplaceAll(data, []byte(`"iterations": 1000`))
	if _, err := backup.Open(bad, "pw"); !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := backup.Seal(backup.Bundle{}, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestAcceptNotABackup(t *testing.T) {
	for _, d := range []string{"", "{}", "hello", `{"format":"magpie-backup","version":99,"kdf":"x"}`} {
		_, err := backup.Open([]byte(d), "pw")
		if err == nil || errors.Is(err, backup.ErrPassphrase) {
			t.Fatalf("%q: %v", d, err)
		}
	}
}

func TestAcceptNoKeysKeepsLocalKey(t *testing.T) {
	machine(t)
	populate(t)
	b, err := backup.Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 2 {
		t.Fatalf("providers %d", len(b.Providers))
	}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) != 0 {
			t.Fatalf("key in keyless: %+v", p)
		}
		if _, ok := p.Headers["X-Api-Key"]; ok {
			t.Fatalf("credential header kept")
		}
		if p.ID == "acme" && p.Headers["X-Team"] != "a" {
			t.Fatalf("plain header lost: %+v", p.Headers)
		}
	}
	data, _ := backup.Seal(b, "pw")

	machine(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Old", Chat: "https://old.example.com/v1", Key: "sk-here"}); err != nil {
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
	if r.Added != 1 || r.Replaced != 1 || !slices.Equal(r.NeedKey, []string{"Beta"}) || r.Settings || r.Profiles != 0 || r.Agents != 0 {
		t.Fatalf("result %+v", r)
	}
	p, _ := provider.Find("acme")
	if p.Key != "sk-here" || p.Chat != "https://acme.example.com/v1" || p.Headers["X-Team"] != "a" {
		t.Fatalf("acme %+v", p)
	}
}

func TestAcceptProfilesMergeAndSettingsOverwrite(t *testing.T) {
	machine(t)
	populate(t)
	data := sealed(t, true, "pw")
	machine(t)
	if err := profile.Save("local", profile.Profile{"codex.model": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("workprofile", profile.Profile{"claude.model": "old"}); err != nil {
		t.Fatal(err)
	}
	b, err := backup.Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(b, backup.Parts{Profiles: true, Settings: true}); err != nil {
		t.Fatal(err)
	}
	ps, _ := profile.Load()
	if ps["workprofile"]["claude.model"] != "acme/m1" || ps["local"]["codex.model"] != "x" {
		t.Fatalf("profiles %+v", ps)
	}
	if all := provider.All(); len(all) != 0 {
		for _, p := range all {
			if p.ID == "acme" {
				t.Fatalf("providers restored though not selected")
			}
		}
	}
	if settings.Load().Theme != "dark" {
		t.Fatal("settings not restored")
	}
}

func TestAcceptReservedIDIgnored(t *testing.T) {
	machine(t)
	b := backup.Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Evil", Chat: "https://evil.example.com/v1", Key: "k"},
		{ID: "good", Name: "Good", Chat: "https://good.example.com/v1", Key: "k"},
	}}
	data, err := backup.Seal(b, "pw")
	if err != nil {
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
	if r.Added != 1 {
		t.Fatalf("result %+v", r)
	}
	if p, err := provider.Find("good"); err != nil || p == nil {
		t.Fatalf("good missing %v", err)
	}
}
