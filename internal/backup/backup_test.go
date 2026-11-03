package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// fresh points every magpie path and the home folder at a new empty place.
func fresh(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// testPNG passes for a picture: its signature is what magpie checks.
var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

func mustSave(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripRestoresEverything(t *testing.T) {
	fresh(t)
	icon, err := provider.StoreIcon(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay-host.example.test/v1",
		Key: "sk-main-AAAA1111", KeyName: "personal", Icon: icon,
		Headers: map[string]string{"X-Api-Key": "hdr-secret-9999", "X-Team": "team-blue"},
	})
	if err := provider.AddKey("relay", "Team", "sk-team-BBBB2222", ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("profile-zeta", profile.Profile{"claude.model": "claude-zeta-model"}); err != nil {
		t.Fatal(err)
	}

	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 {
		t.Fatalf("version %d, want 1", b.Version)
	}
	sealed, err := Seal(b, "pw-round-trip")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-main-AAAA1111", "sk-team-BBBB2222", "hdr-secret-9999", "relay-host.example.test", "profile-zeta", "claude-zeta-model"} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatalf("sealed file holds plaintext %q", secret)
		}
	}

	// a new machine
	fresh(t)
	got, err := Open(sealed, "pw-round-trip")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 0 || !res.Settings || res.Profiles != 1 {
		t.Fatalf("result %+v", res)
	}
	if len(res.NeedKey) != 0 {
		t.Fatalf("NeedKey %v, want none", res.NeedKey)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-main-AAAA1111" || p.KeyName != "personal" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-team-BBBB2222" {
		t.Fatalf("keys not restored: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-secret-9999" || p.Headers["X-Team"] != "team-blue" {
		t.Fatalf("headers not restored: %v", p.Headers)
	}
	data, err := os.ReadFile(provider.IconFile(strings.TrimPrefix(p.Icon, "file:")))
	if err != nil || !bytes.Equal(data, testPNG) {
		t.Fatalf("icon not restored: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "en" {
		t.Fatalf("settings %+v", s)
	}
	ps, err := profile.Load()
	if err != nil || ps["profile-zeta"]["claude.model"] != "claude-zeta-model" {
		t.Fatalf("profiles %v %v", ps, err)
	}
}

func TestNoKeysStripsCredentialsOnly(t *testing.T) {
	fresh(t)
	mustSave(t, provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay-host.example.test/v1", Key: "sk-main-AAAA1111",
		Headers: map[string]string{"X-Api-Key": "hdr-secret-9999", "X-Team": "team-blue", "Cookie": "c=1"},
	})
	if err := provider.AddKey("relay", "Team", "sk-team-BBBB2222", ""); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 {
		t.Fatalf("providers %d", len(b.Providers))
	}
	p := b.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Fatalf("keys left in backup: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Fatal("X-Api-Key kept")
	}
	if _, ok := p.Headers["Cookie"]; ok {
		t.Fatal("Cookie kept")
	}
	if p.Headers["X-Team"] != "team-blue" {
		t.Fatalf("X-Team lost: %v", p.Headers)
	}
	sealed, err := Seal(b, "pw-no-keys")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("sk-")) || bytes.Contains(sealed, []byte("hdr-secret")) {
		t.Fatal("no-keys backup holds a secret")
	}
}

func TestWrongPassphraseAndTamperedIterations(t *testing.T) {
	fresh(t)
	sealed, err := Seal(Bundle{Version: 1}, "pw-right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(sealed, "pw-wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	for _, iters := range []string{"100000", "1000"} {
		tampered := bytes.Replace(sealed, []byte(`"iterations": 600000`), []byte(`"iterations": `+iters), 1)
		if bytes.Equal(tampered, sealed) {
			t.Fatal("test did not change the header")
		}
		if _, err := Open(tampered, "pw-right"); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("iterations %s accepted: %v", iters, err)
		}
	}
	if _, err := Open(sealed, "pw-right"); err != nil {
		t.Fatalf("untouched file: %v", err)
	}
}

func TestDefaultIterationsAboveFloor(t *testing.T) {
	sealed, err := Seal(Bundle{Version: 1}, "pw-floor")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sealed, []byte(`"iterations": 600000`)) {
		t.Fatalf("default iterations not 600000: %s", sealed)
	}
}

func TestSealRefusesEmptyPassphrase(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("empty passphrase sealed")
	}
}

func TestFormatErrorsAreNotPassphraseErrors(t *testing.T) {
	cases := map[string][]byte{
		"empty":    nil,
		"text":     []byte("hello"),
		"empty {}": []byte("{}"),
	}
	for name, data := range cases {
		_, err := Open(data, "pw")
		if err == nil || errors.Is(err, ErrPassphrase) {
			t.Fatalf("%s: err %v", name, err)
		}
		if !errors.Is(err, ErrNotBackup) {
			t.Fatalf("%s: not reported as not-a-backup: %v", name, err)
		}
		checkNoPassphraseWords(t, err)
	}

	newer := []byte(`{"format": "magpie-backup", "version": 2, "kdf": "unknown", "iterations": 1}`)
	_, err := Open(newer, "pw")
	if err == nil || errors.Is(err, ErrPassphrase) || !errors.Is(err, ErrNewer) {
		t.Fatalf("newer version: %v", err)
	}
	checkNoPassphraseWords(t, err)
}

func checkNoPassphraseWords(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "passphrase") || strings.Contains(msg, "口令") {
		t.Fatalf("error text mentions the passphrase: %q", msg)
	}
}

func TestKeylessRestoreKeepsLocalKey(t *testing.T) {
	fresh(t)
	mustSave(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://old-host.example.test/v1", Key: "sk-local-CCCC3333"})

	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "Relay", Chat: "https://new-host.example.test/v1"},
		{ID: "other", Name: "Other Vendor", Chat: "https://other-host.example.test/v1"},
	}}
	res, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 1 {
		t.Fatalf("counts %+v", res)
	}
	if !reflect.DeepEqual(res.NeedKey, []string{"Other Vendor"}) {
		t.Fatalf("NeedKey %v", res.NeedKey)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-local-CCCC3333" || p.Chat != "https://new-host.example.test/v1" {
		t.Fatalf("local key not kept or URL not updated: %+v", p)
	}
}

func TestInvalidAndReservedIDsIgnored(t *testing.T) {
	fresh(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Chat: "https://a.example.test/v1", Key: "k"},
		{ID: "Bad Id!", Name: "Bad", Chat: "https://b.example.test/v1", Key: "k"},
		{ID: "", Name: "Empty", Chat: "https://c.example.test/v1", Key: "k"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 {
		t.Fatalf("invalid ids counted: %+v", res)
	}
}

func TestPartsSelectionLeavesOthersAlone(t *testing.T) {
	fresh(t)
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "relay", Name: "Relay", Chat: "https://r.example.test/v1", Key: "k"}},
		Settings:  &settings.Settings{Theme: "dark"},
		Profiles:  map[string]profile.Profile{"p1": {"claude.model": "m"}},
	}
	res, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings || res.Profiles != 0 || res.Agents != 0 || res.Added != 1 {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		t.Fatal("settings were written though not selected")
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Fatalf("profiles written though not selected: %v", ps)
	}
}

func TestNoSettingsInBundleKeepsLocalSettings(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings || settings.Load().Theme != "light" {
		t.Fatalf("local settings touched: %+v", res)
	}
}

func TestAgentsNotInstalledAreSkipped(t *testing.T) {
	fresh(t)
	b := Bundle{Version: 1, Agents: profile.Profile{"codex.model": "gpt-test", "nonesuch.model": "x"}}
	res, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 0 {
		t.Fatalf("agents changed: %+v", res)
	}
	joined := strings.Join(res.Skipped, "\n")
	if !strings.Contains(joined, "codex") || !strings.Contains(joined, "本机未安装") || !strings.Contains(joined, "nonesuch") {
		t.Fatalf("skipped %v", res.Skipped)
	}
}

func TestInstalledAgentModelIsRestored(t *testing.T) {
	fresh(t)
	home := os.Getenv("HOME")
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Agents: profile.Profile{"claude.model": "claude-restored-model"}}
	res, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 1 {
		t.Fatalf("agents %d, skipped %v", res.Agents, res.Skipped)
	}
	if v, _ := edit.GetJSON(path, "model"); v != "claude-restored-model" {
		t.Fatalf("model %q", v)
	}
}
