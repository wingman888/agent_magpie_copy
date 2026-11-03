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

// isolate points every magpie file and the agents' homes at a temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1", Key: "sk-roundtrip-secret",
	}}}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sk-roundtrip-secret")) || bytes.Contains(data, []byte("api.acme.test")) {
		t.Fatal("plaintext secret or address visible in the backup file")
	}
	got, err := Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Key != "sk-roundtrip-secret" || got.Providers[0].Chat != "https://api.acme.test/v1" {
		t.Fatalf("round trip lost data: %+v", got.Providers)
	}
	if got.Version != FormatVersion {
		t.Errorf("version = %d", got.Version)
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(data, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
}

func TestIterationsTamperRejected(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"100000", "700000", "600001"} {
		bad := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": `+n), 1)
		if bytes.Equal(bad, data) {
			t.Fatal("test did not change the header")
		}
		if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
			t.Errorf("iterations %s: err = %v, want ErrPassphrase", n, err)
		}
	}
}

func TestOtherHeaderTamperRejected(t *testing.T) {
	data, err := Seal(Bundle{Version: 1, Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.test"}}}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Replace(data, []byte(`"kdf": "pbkdf2-sha256"`), []byte(`"kdf": "pbkdf2-sha1"`), 1)
	if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("kdf tamper: err = %v", err)
	}
	cut := data[:len(data)-20]
	if _, err := Open(cut, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("truncated body: err = %v", err)
	}
}

func TestNotABackup(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"format":"other"}`, "[1,2]"} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Errorf("%q: no error", in)
			continue
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("%q: reported as wrong passphrase: %v", in, err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("%q: format error mentions the passphrase: %v", in, err)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestNewerVersionAsksForUpgrade(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	newer := bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 2`), 1)
	_, err = Open(newer, "pw")
	if err == nil {
		t.Fatal("version 2 accepted")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatalf("version 2 reported as wrong passphrase: %v", err)
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("err = %v, want an upgrade hint", err)
	}
}

func TestCollectNoKeysStripsCredentials(t *testing.T) {
	isolate(t)
	err := provider.Save(provider.Provider{
		ID: "zebra", Name: "Zebra Relay", Chat: "https://api.zebra.test/v1",
		Key:     "sk-live-secret-one",
		Keys:    []provider.KeyAccount{{Name: "team", Key: "sk-live-secret-two"}},
		Headers: map[string]string{"X-Api-Key": "hdr-secret-value", "X-Team": "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work-profile-xyz", profile.Profile{"claude.model": "opus"}); err != nil {
		t.Fatal(err)
	}

	withKeys, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(withKeys.Providers) != 1 || withKeys.Providers[0].Key != "sk-live-secret-one" || len(withKeys.Providers[0].Keys) != 1 {
		t.Fatalf("keys lost: %+v", withKeys.Providers)
	}
	if _, ok := withKeys.Providers[0].Headers["X-Api-Key"]; !ok {
		t.Error("credential header dropped when keys are included")
	}

	noKeys, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := noKeys.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Errorf("no-keys bundle still has key material: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Error("credential header kept")
	}
	if p.Headers["X-Team"] != "blue" {
		t.Errorf("plain header lost: %v", p.Headers)
	}

	data, err := Seal(noKeys, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-live-secret", "hdr-secret-value", "api.zebra.test", "work-profile-xyz"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("backup contains %q in plain text", s)
		}
	}
}

func TestRestoreProviderRules(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1", Key: "local-key"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "acme", Name: "Acme New", Chat: "https://new.acme.test/v1"}, // no key: keeps the local one
		{ID: "fresh", Name: "Fresh", Chat: "https://fresh.test/v1"},      // no key anywhere: needs one
		{ID: "ok-two", Name: "Two", Chat: "https://two.test/v1", Key: "k2"},
		{ID: "magpie", Name: "Gateway", Chat: "https://gw.test/v1", Key: "k3"}, // reserved
		{ID: "Bad Id", Name: "Bad", Chat: "https://bad.test/v1", Key: "k4"},   // invalid
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Replaced != 1 {
		t.Errorf("Added=%d Replaced=%d, want 2 and 1", r.Added, r.Replaced)
	}
	if len(r.NeedKey) != 1 || r.NeedKey[0] != "Fresh" {
		t.Errorf("NeedKey = %v, want [Fresh]", r.NeedKey)
	}
	acme, err := provider.Find("acme")
	if err != nil {
		t.Fatal(err)
	}
	if acme.Key != "local-key" || acme.Chat != "https://new.acme.test/v1" || acme.Name != "Acme New" {
		t.Errorf("acme = %+v: want the new fields with the local key", acme)
	}
	if _, err := provider.Find("magpie"); err == nil {
		t.Error("reserved id stored")
	}
	if _, err := provider.Find("bad id"); err == nil {
		t.Error("invalid id stored")
	}
}

// A local server with no key is still listed as needing one: the rule is
// literal, with no exemption for local addresses.
func TestRestoreNeedKeyLocalServer(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "local", Name: "Local Llama", Chat: "http://localhost:11434/v1"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 {
		t.Errorf("Added = %d, want 1", r.Added)
	}
	if len(r.NeedKey) != 1 || r.NeedKey[0] != "Local Llama" {
		t.Errorf("NeedKey = %v, want [Local Llama]", r.NeedKey)
	}
}

func TestRestoreSelectedPartsOnly(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1", Key: "local-key"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("keep", profile.Profile{"claude.model": "local"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "fresh", Name: "Fresh", Chat: "https://fresh.test/v1", Key: "k"}},
		Settings:  &settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"},
		Profiles:  map[string]profile.Profile{"work": {"claude.model": "opus"}},
		Agents:    map[string]map[string]string{"nosuchagent": {"model": "x"}},
	}
	r, err := Restore(b, Parts{Profiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Replaced != 0 || r.Settings || r.Agents != 0 || len(r.Skipped) != 0 || r.Profiles != 1 {
		t.Errorf("result = %+v", r)
	}
	if _, err := provider.Find("fresh"); err == nil {
		t.Error("providers restored though not selected")
	}
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ps["keep"]; !ok {
		t.Error("existing profile removed")
	}
	if _, ok := ps["work"]; !ok {
		t.Error("profile not written")
	}
}

func TestRestoreSettingsAndAgents(t *testing.T) {
	home := isolate(t)
	b := Bundle{Version: 1,
		Settings: &settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"},
		Agents:   map[string]map[string]string{"nosuchagent": {"model": "x"}},
	}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Settings || settings.Load().Theme != "dark" {
		t.Errorf("settings not restored: %+v", r)
	}
	if r.Agents != 0 || len(r.Skipped) != 1 {
		t.Errorf("agents: changed=%d skipped=%v", r.Agents, r.Skipped)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "magpie", "settings.json")); err != nil {
		t.Error(err)
	}

	// A bundle with no settings leaves the local ones alone.
	r, err = Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings || settings.Load().Theme != "dark" {
		t.Errorf("settings overwritten by a bundle without them: %+v", r)
	}
}

func TestRestoreRejectsNewerBundle(t *testing.T) {
	isolate(t)
	if _, err := Restore(Bundle{Version: FormatVersion + 1}, All); err == nil {
		t.Fatal("newer bundle restored")
	}
}

func TestIconsCarriedAndKeptLocal(t *testing.T) {
	isolate(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-icon-one")
	ref, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "pic", Name: "Pic", Chat: "https://pic.test/v1", Key: "k", Icon: ref}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if !bytes.Equal(b.Icons[name], png) {
		t.Fatalf("icon not in the bundle: %v", b.Icons)
	}

	// On a fresh machine the picture is written back under the same name.
	if err := os.Remove(provider.IconFile(name)); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(provider.IconFile(name)); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("icon not restored: %v", err)
	}
}
