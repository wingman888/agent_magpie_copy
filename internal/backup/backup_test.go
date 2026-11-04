package backup

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// isolate points every magpie file and the home directory at a fresh temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func pngBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
}

func sampleBundle() Bundle {
	return Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
			Key:     "sk-SECRET-123456",
			KeyName: "personal",
			Keys:    []provider.KeyAccount{{Name: "team", Key: "sk-TEAM-998877"}},
			Headers: map[string]string{"X-Team": "platform", "X-Api-Key": "hdr-SECRET-42"},
		}},
		Profiles: map[string]profile.Profile{"profile-zq7": {"claude.model": "deepseek/pro"}},
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	isolate(t)
	b := sampleBundle()
	data, err := Seal(b, "pw-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sk-SECRET-123456", "sk-TEAM-998877", "hdr-SECRET-42", "api.deepseek.com", "profile-zq7", "platform", "DeepSeek"} {
		if bytes.Contains(data, []byte(leak)) {
			t.Errorf("sealed file leaks %q", leak)
		}
	}
	m := regexp.MustCompile(`"iterations"\s*:\s*(\d+)`).FindSubmatch(data)
	if m == nil || string(m[1]) != "600000" {
		t.Fatalf("header iterations = %q, want 600000", m)
	}
	got, err := Open(data, "pw-one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Providers) != 1 {
		t.Fatalf("unexpected bundle: %+v", got)
	}
	p := got.Providers[0]
	if p.Key != "sk-SECRET-123456" || p.KeyName != "personal" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-TEAM-998877" {
		t.Errorf("keys not preserved: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-SECRET-42" || p.Headers["X-Team"] != "platform" {
		t.Errorf("headers not preserved: %v", p.Headers)
	}
	if got.Profiles["profile-zq7"]["claude.model"] != "deepseek/pro" {
		t.Errorf("profiles not preserved: %v", got.Profiles)
	}
}

func TestSealEmptyPassphrase(t *testing.T) {
	if _, err := Seal(sampleBundle(), ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, err := Seal(sampleBundle(), "right")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
}

func TestIterationsTamperedRejected(t *testing.T) {
	data, err := Seal(sampleBundle(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte(`"iterations":600000`), []byte(`"iterations":100000`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("test setup: iterations not found in header")
	}
	if _, err := Open(tampered, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
}

func TestHeaderFieldTamperedRejected(t *testing.T) {
	data, err := Seal(sampleBundle(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte(`"kdf":"pbkdf2-sha256"`), []byte(`"kdf":"pbkdf2-sha256 "`), 1)
	if _, err := Open(tampered, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
}

func TestNotBackupFiles(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"format":"other","version":1}`} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Fatalf("%q: no error", in)
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("%q: classified as passphrase error", in)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%q: err = %v", in, err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("%q: message mentions passphrase: %v", in, err)
		}
	}
}

func TestNewerVersionRejected(t *testing.T) {
	data, err := Seal(sampleBundle(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	newer := bytes.Replace(data, []byte(`"version":1,`), []byte(`"version":2,`), 1)
	_, err = Open(newer, "pw")
	if err == nil {
		t.Fatal("version 2 accepted")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatal("version 2 classified as passphrase error")
	}
	if !strings.Contains(err.Error(), "需升级 magpie") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Errorf("message mentions passphrase: %v", err)
	}
}

func TestCollectNoKeys(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{
		ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-1",
		Headers: map[string]string{"X-Api-Key": "h1", "X-Team": "t1", "Cookie-Jar": "c", "X-Session-Id": "s", "X-Pw-Password": "p", "X-Secret": "x", "X-Token": "t"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("deepseek", "team", "sk-2", ""); err != nil {
		t.Fatal(err)
	}

	withKeys, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if withKeys.Version != 1 || len(withKeys.Providers) != 1 || withKeys.Providers[0].Key != "sk-1" {
		t.Fatalf("with keys: %+v", withKeys.Providers)
	}

	noKeys, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := noKeys.Providers[0]
	if p.Key != "" {
		t.Errorf("key kept: %q", p.Key)
	}
	for _, k := range p.Keys {
		if k.Key != "" {
			t.Errorf("account key kept: %q", k.Key)
		}
	}
	if len(p.Headers) != 1 || p.Headers["X-Team"] != "t1" {
		t.Errorf("headers = %v, want only X-Team", p.Headers)
	}
}

func TestRestoreKeepsLocalKey(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://old.example/v1", Key: "local-key"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("deepseek", "spare", "local-spare", ""); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Chat: "https://new.example/v1"},
		{ID: "relay", Name: "Relay", Anthropic: "https://relay.example"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Replaced != 1 || r.Added != 1 {
		t.Errorf("added/replaced = %d/%d, want 1/1", r.Added, r.Replaced)
	}
	if len(r.NeedKey) != 1 || r.NeedKey[0] != "Relay" {
		t.Errorf("NeedKey = %v, want [Relay]", r.NeedKey)
	}
	ds, err := provider.Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Key != "local-key" || ds.Chat != "https://new.example/v1" || len(ds.Keys) != 1 {
		t.Errorf("deepseek after restore: key=%q chat=%q keys=%d", ds.Key, ds.Chat, len(ds.Keys))
	}
}

func TestRestoreIgnoresInvalidIDs(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "M", Chat: "https://a.example/v1", Key: "k"},
		{ID: "Bad Id", Name: "B", Chat: "https://a.example/v1", Key: "k"},
		{ID: "", Name: "E", Chat: "https://a.example/v1", Key: "k"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v, want nothing counted", r)
	}
	if len(provider.Stored()) != 0 {
		t.Errorf("stored providers = %v", provider.Stored())
	}
}

func TestRestorePartsOnly(t *testing.T) {
	isolate(t)
	b := sampleBundle()
	b.Settings = &settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}
	r, err := Restore(b, Parts{Profiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings || r.Agents != 0 || r.Added != 0 || r.Replaced != 0 {
		t.Errorf("unselected parts touched: %+v", r)
	}
	if r.Profiles != 1 {
		t.Errorf("profiles = %d, want 1", r.Profiles)
	}
	if _, err := provider.Find("deepseek"); err == nil {
		t.Error("provider restored though Providers was not selected")
	}
	if s := settings.Load(); s.Theme != "system" {
		t.Errorf("settings changed: %+v", s)
	}
}

func TestRestoreVersionOneProvidersOnly(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k"}}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Settings || r.Profiles != 0 || r.Agents != 0 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v", r)
	}
}

func TestRoundTripIconsSettingsProfiles(t *testing.T) {
	isolate(t)
	name, err := provider.StoreIcon(pngBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-1", Icon: name}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("profile-zq7", profile.Profile{"claude.model": "deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("profile-zq7")) {
		t.Error("profile name in sealed file")
	}
	got, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}

	// Fresh machine.
	isolate(t)
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || !r.Settings || r.Profiles != 1 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v", r)
	}
	if s := settings.Load(); s.Theme != "dark" {
		t.Errorf("settings theme = %q", s.Theme)
	}
	if ps, _ := profile.Load(); ps["profile-zq7"]["claude.model"] != "deepseek/pro" {
		t.Errorf("profiles = %v", ps)
	}
	if p, err := provider.Find("deepseek"); err != nil || p.Icon != name || p.Key != "sk-1" {
		t.Fatalf("provider = %+v, err %v", p, err)
	}
	path := provider.IconFile(strings.TrimPrefix(name, "file:"))
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(content, pngBytes()) {
		t.Errorf("icon file not restored at %q: %v", path, err)
	}
}

func TestRestoreKeepsExistingIcon(t *testing.T) {
	isolate(t)
	name, err := provider.StoreIcon(pngBytes())
	if err != nil {
		t.Fatal(err)
	}
	path := provider.IconFile(strings.TrimPrefix(name, "file:"))
	// Same name, different bytes on this machine: the local one stays.
	if err := os.WriteFile(path, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Icons: map[string][]byte{strings.TrimPrefix(name, "file:"): pngBytes()},
		Providers: []provider.Provider{{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Icon: name}}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(path); string(content) != "local" {
		t.Errorf("existing icon overwritten: %q", content)
	}
}
