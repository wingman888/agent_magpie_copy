package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// useMachine points magpie's config at a fresh, empty directory.
func useMachine(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func mustSave(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatalf("save provider %s: %v", p.ID, err)
	}
}

const testPass = "correct horse battery"

func sampleBundle() Bundle {
	return Bundle{
		Version: 1,
		App:     "test",
		Providers: []provider.Provider{{
			ID:      "relay-one",
			Name:    "Relay One",
			Chat:    "https://relay.example.org/v1",
			Key:     "sk-primary-SECRET-0001",
			KeyName: "Personal",
			Keys:    []provider.KeyAccount{{Name: "Team", Key: "sk-team-SECRET-0002"}},
			Headers: map[string]string{"X-Api-Key": "hdr-SECRET-0003", "X-Team": "blue"},
			Icon:    "file:0123456789abcdef.png",
		}},
		Icons:    map[string][]byte{"0123456789abcdef.png": []byte("\x89PNG\r\n\x1a\nicon")},
		Settings: &settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"},
		Profiles: map[string]profile.Profile{"work-plan": {"claude.model": "relay-one/big"}},
		Agents:   map[string]map[string]string{"claude": {"model": "relay-one/big"}},
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	useMachine(t)
	want := sampleBundle()
	data, err := Seal(want, testPass)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, testPass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 1 || len(got.Providers) != 1 {
		t.Fatalf("bundle = %+v", got)
	}
	p := got.Providers[0]
	if p.Key != "sk-primary-SECRET-0001" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-team-SECRET-0002" {
		t.Errorf("keys not preserved: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-SECRET-0003" || p.Headers["X-Team"] != "blue" {
		t.Errorf("headers not preserved: %+v", p.Headers)
	}
	if !bytes.Equal(got.Icons["0123456789abcdef.png"], want.Icons["0123456789abcdef.png"]) {
		t.Errorf("icon bytes not preserved")
	}
	if got.Settings == nil || got.Settings.Theme != "dark" {
		t.Errorf("settings not preserved: %+v", got.Settings)
	}
	if got.Profiles["work-plan"]["claude.model"] != "relay-one/big" {
		t.Errorf("profiles not preserved: %+v", got.Profiles)
	}
	if got.Agents["claude"]["model"] != "relay-one/big" {
		t.Errorf("agents not preserved: %+v", got.Agents)
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Seal(sampleBundle(), ""); err == nil {
		t.Fatal("Seal with empty passphrase: want error")
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: err = %v, want ErrPassphrase", err)
	}
}

func TestTamperedIterations(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("header does not hold iterations as written:\n%s", data)
	}
	for _, n := range []string{"100000", "600001", "1000"} {
		tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": `+n), 1)
		_, err := Open(tampered, testPass)
		if !errors.Is(err, ErrPassphrase) {
			t.Errorf("iterations=%s: err = %v, want ErrPassphrase", n, err)
		}
	}
}

func TestTamperedHeaderField(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte(`"kdf": "pbkdf2-sha256"`), []byte(`"kdf": "pbkdf2-sha256 "`), 1)
	if _, err := Open(tampered, testPass); !errors.Is(err, ErrPassphrase) {
		t.Errorf("kdf altered: err = %v, want ErrPassphrase", err)
	}
}

func TestNotABackup(t *testing.T) {
	for _, in := range []string{"", "   \n", "hello", "{}", `{"format":"something-else","version":1}`} {
		_, err := Open([]byte(in), testPass)
		if err == nil {
			t.Errorf("%q: want error", in)
			continue
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("%q: got ErrPassphrase, want a not-a-backup error", in)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%q: err = %q, want not-a-backup text", in, err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("%q: error text leaks passphrase wording: %q", in, err)
		}
	}
}

func TestNewerVersion(t *testing.T) {
	data := []byte(`{"format":"magpie-backup","version":2,"kdf":"from-the-future"}`)
	_, err := Open(data, testPass)
	if err == nil {
		t.Fatal("version 2: want error")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatal("version 2: got ErrPassphrase, want an upgrade error")
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("err = %q, want upgrade hint", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Errorf("error text leaks passphrase wording: %q", err)
	}
}

func TestFileHasNoPlaintext(t *testing.T) {
	data, err := Seal(sampleBundle(), testPass)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-primary-SECRET-0001", "sk-team-SECRET-0002", "hdr-SECRET-0003", "relay.example.org", "work-plan", "relay-one", "Relay One", "Personal"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("sealed file contains plaintext %q", s)
		}
	}
}

func TestCollectNoKeys(t *testing.T) {
	useMachine(t)
	mustSave(t, provider.Provider{
		ID: "relay-one", Name: "Relay One", Chat: "https://relay.example.org/v1",
		Key: "sk-primary-SECRET-0001", KeyName: "Personal",
		Headers: map[string]string{"X-Api-Key": "hdr-1", "X-Team": "blue", "Cookie": "c=1", "X-Session-Id": "s"},
	})
	if err := provider.AddKey("relay-one", "Team", "sk-team-SECRET-0002", ""); err != nil {
		t.Fatal(err)
	}

	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || len(b.Providers) != 1 {
		t.Fatalf("bundle = %+v", b)
	}
	p := b.Providers[0]
	if p.Key != "" || p.KeyName != "" || len(p.Keys) != 0 {
		t.Errorf("keys leaked: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Errorf("credential header kept: %+v", p.Headers)
	}
	if _, ok := p.Headers["Cookie"]; ok {
		t.Errorf("cookie header kept: %+v", p.Headers)
	}
	if _, ok := p.Headers["X-Session-Id"]; ok {
		t.Errorf("session header kept: %+v", p.Headers)
	}
	if p.Headers["X-Team"] != "blue" {
		t.Errorf("plain header dropped: %+v", p.Headers)
	}

	full, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if full.Providers[0].Key != "sk-primary-SECRET-0001" || len(full.Providers[0].Keys) != 1 {
		t.Errorf("keys=true lost keys: %+v", full.Providers[0])
	}
	if full.Providers[0].Headers["X-Api-Key"] != "hdr-1" {
		t.Errorf("keys=true lost headers: %+v", full.Providers[0].Headers)
	}
}

func TestCollectIcon(t *testing.T) {
	useMachine(t)
	png := []byte("\x89PNG\r\n\x1a\nfake-picture-bytes")
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, provider.Provider{ID: "withicon", Name: "With Icon", Chat: "https://a.example.org/v1", Key: "k", Icon: icon})
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(icon, "file:")
	if !bytes.Equal(b.Icons[name], png) {
		t.Errorf("icon bytes not collected for %s: %v", name, b.Icons)
	}
}

func TestRestoreKeepsLocalKey(t *testing.T) {
	useMachine(t)
	mustSave(t, provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://old.example.org/v1", Key: "local-key", KeyName: "Mine"})
	if err := provider.AddKey("deepseek", "Second", "local-second", ""); err != nil {
		t.Fatal(err)
	}

	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "deepseek", Name: "DeepSeek New", Chat: "https://new.example.org/v1"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 1 || res.Added != 0 {
		t.Errorf("counts = added %d replaced %d", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 0 {
		t.Errorf("NeedKey = %v, want none: local key is kept", res.NeedKey)
	}
	p, err := provider.Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "local-key" || p.KeyName != "Mine" || len(p.Keys) != 1 || p.Keys[0].Key != "local-second" {
		t.Errorf("local keys lost: %+v", p)
	}
	if p.Chat != "https://new.example.org/v1" || p.Name != "DeepSeek New" {
		t.Errorf("other fields not updated: %+v", p)
	}
}

func TestRestoreAddsAndNeedsKey(t *testing.T) {
	useMachine(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "fresh-one", Name: "Fresh One", Chat: "https://fresh.example.org/v1"},
		{ID: "Bad Id", Name: "Bad", Chat: "https://bad.example.org/v1", Key: "k"},
		{ID: "magpie", Name: "Gateway", Chat: "https://g.example.org/v1", Key: "k"},
		{ID: "", Name: "Empty", Chat: "https://e.example.org/v1", Key: "k"},
	}}
	res, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 0 {
		t.Errorf("counts = added %d replaced %d, want 1 and 0", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 1 || res.NeedKey[0] != "Fresh One" {
		t.Errorf("NeedKey = %v, want [Fresh One]", res.NeedKey)
	}
	if _, err := provider.Find("magpie"); err == nil {
		t.Error("reserved id magpie was written")
	}
}

func TestRestoreIconWrittenOnlyWhenMissing(t *testing.T) {
	useMachine(t)
	png := []byte("\x89PNG\r\n\x1a\nfrom-backup")
	name := "0123456789abcdef.png"
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "iconned", Name: "Iconned", Chat: "https://i.example.org/v1", Key: "k", Icon: "file:" + name}},
		Icons:     map[string][]byte{name: png},
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(provider.IconFile(name))
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("icon not written: %v", err)
	}

	// an existing picture of the same name is not overwritten
	if err := os.WriteFile(provider.IconFile(name), []byte("\x89PNG\r\n\x1a\nlocal"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(provider.IconFile(name))
	if !bytes.Contains(got, []byte("local")) {
		t.Errorf("existing icon overwritten: %q", got)
	}
}

func TestRestorePartsUnselectedAreZero(t *testing.T) {
	useMachine(t)
	s := settings.Settings{Theme: "dark"}
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "p-one", Name: "P One", Chat: "https://p.example.org/v1", Key: "k"}},
		Settings:  &s,
		Profiles:  map[string]profile.Profile{"plan": {"claude.model": "x"}},
		Agents:    map[string]map[string]string{"nosuchagent": {"model": "x"}},
	}
	res, err := Restore(b, Parts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 || res.Settings || res.Profiles != 0 || res.Agents != 0 || len(res.Skipped) != 0 {
		t.Errorf("unselected parts changed result: %+v", res)
	}
	if _, err := provider.Find("p-one"); err == nil {
		t.Error("provider written although Providers not selected")
	}
	if names, _ := profile.Load(); len(names) != 0 {
		t.Errorf("profile written although Profiles not selected: %v", names)
	}
}

func TestRestoreSettingsProfilesAgents(t *testing.T) {
	useMachine(t)
	s := settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}
	b := Bundle{Version: 1,
		Settings: &s,
		Profiles: map[string]profile.Profile{"plan": {"claude.model": "x"}, "other": {"codex.model": "y"}},
		Agents:   map[string]map[string]string{"nosuchagent": {"model": "x"}},
	}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Settings || res.Profiles != 2 {
		t.Errorf("settings %v profiles %d, want true and 2", res.Settings, res.Profiles)
	}
	if got := settings.Load(); got.Theme != "dark" {
		t.Errorf("settings not written: %+v", got)
	}
	ps, err := profile.Load()
	if err != nil || len(ps) != 2 {
		t.Errorf("profiles = %v, %v", ps, err)
	}
	found := false
	for _, s := range res.Skipped {
		if strings.Contains(s, "nosuchagent") {
			found = true
		}
	}
	if !found {
		t.Errorf("uninstalled agent not listed as skipped: %v", res.Skipped)
	}
}

func TestRestoreNoSettingsKeepsLocal(t *testing.T) {
	useMachine(t)
	if err := settings.Save(settings.Settings{Theme: "light", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings {
		t.Error("Settings reported restored although the bundle has none")
	}
	if got := settings.Load(); got.Theme != "light" {
		t.Errorf("local settings overwritten: %+v", got)
	}
}

func TestCollectRestoreEndToEnd(t *testing.T) {
	useMachine(t)
	mustSave(t, provider.Provider{ID: "relay-one", Name: "Relay One", Chat: "https://relay.example.org/v1", Key: "sk-primary-SECRET-0001",
		Headers: map[string]string{"X-Api-Key": "hdr-1", "X-Team": "blue"}})
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, testPass)
	if err != nil {
		t.Fatal(err)
	}

	useMachine(t) // a brand-new machine
	opened, err := Open(data, testPass)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || len(res.NeedKey) != 0 {
		t.Errorf("result = %+v", res)
	}
	p, err := provider.Find("relay-one")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-primary-SECRET-0001" || p.Headers["X-Api-Key"] != "hdr-1" {
		t.Errorf("restored provider = %+v", p)
	}
}
