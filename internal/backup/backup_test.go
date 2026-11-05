package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// isolate points HOME and the config dirs at a fresh folder, and empties
// PATH so no agent counts as installed.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	return home
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-live-123456789",
	}}}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, "correct horse")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Key != "sk-live-123456789" || got.Providers[0].Chat != "https://relay.example.com/v1" {
		t.Fatalf("round trip lost data: %+v", got.Providers)
	}
}

func TestSealDefaultIterations(t *testing.T) {
	data, err := Seal(Bundle{Version: 1}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("header does not carry 600000 iterations: %s", data)
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("empty password sealed")
	}
}

func TestSecretsNotInFile(t *testing.T) {
	b := Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID: "secretrelay", Name: "Secret Relay", Chat: "https://secret-vendor.example/v1",
			Key: "sk-SECRET-KEY-42", Keys: []provider.KeyAccount{{Name: "team", Key: "sk-OTHER-KEY-99"}},
			Headers: map[string]string{"X-Team": "teamvalue-xyz"},
		}},
		Profiles: map[string]profile.Profile{"work-profile-name": {"claude.model": "m"}},
	}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-SECRET-KEY-42", "sk-OTHER-KEY-99", "teamvalue-xyz", "secret-vendor.example", "work-profile-name", "secretrelay"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("backup file contains %q in plain text", s)
		}
	}
}

func TestWrongPassphrase(t *testing.T) {
	data, _ := Seal(Bundle{Version: 1}, "right")
	_, err := Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong password: err = %v", err)
	}
}

func TestTamperedIterationsRejected(t *testing.T) {
	data, err := Seal(Bundle{Version: 1, Providers: []provider.Provider{{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k"}}}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("test did not change the header")
	}
	_, err = Open(tampered, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("lowered iterations: err = %v, want ErrPassphrase", err)
	}
}

func TestRestoreKeepsLocalCredentialHeaders(t *testing.T) {
	isolate(t)
	if err := provider.WriteStored([]provider.Provider{{
		ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-local",
		Headers: map[string]string{"X-Api-Key": "local-secret", "X-Old": "gone"},
	}}); err != nil {
		t.Fatal(err)
	}
	// a keyless bundle that (improbably) also carries a credential header of the same name
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "relay", Name: "Relay", Chat: "https://relay.example/v2",
		Headers: map[string]string{"X-Team": "blue", "X-Api-Key": "bundle-secret"},
	}}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	p := provider.Stored()[0]
	if p.Key != "sk-local" {
		t.Errorf("local key lost: %q", p.Key)
	}
	if len(p.Headers) != 2 || p.Headers["X-Team"] != "blue" || p.Headers["X-Api-Key"] != "local-secret" {
		t.Errorf("headers = %v, want X-Team from bundle and X-Api-Key from this machine", p.Headers)
	}
	if _, ok := p.Headers["X-Old"]; ok {
		t.Errorf("ordinary local header kept, should follow the bundle: %v", p.Headers)
	}
	if p.Chat != "https://relay.example/v2" {
		t.Errorf("chat not updated: %q", p.Chat)
	}
}

func TestRestoreNeedKeyWithoutEndpoint(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "bare", Name: "Bare Vendor"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NeedKey) != 1 || res.NeedKey[0] != "Bare Vendor" {
		t.Errorf("NeedKey = %v, want [Bare Vendor]", res.NeedKey)
	}
}

func TestCollectNoKeysStripsProxyUserinfo(t *testing.T) {
	isolate(t)
	if err := settings.Save(settings.Settings{Proxy: "http://user:pw-secret@proxy.example:7890"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Settings == nil || b.Settings.Proxy != "http://proxy.example:7890" {
		t.Fatalf("proxy with keys=false = %+v, want userinfo removed", b.Settings)
	}
	b, err = Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Settings == nil || b.Settings.Proxy != "http://user:pw-secret@proxy.example:7890" {
		t.Fatalf("proxy with keys=true = %+v, want unchanged", b.Settings)
	}
}

func TestNotBackup(t *testing.T) {
	for _, in := range []string{"", "   \n", "hello", "{}", `{"format":"other"}`} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Errorf("Open(%q) accepted", in)
			continue
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q) reported a password error: %v", in, err)
		}
		if !errors.Is(err, ErrNotBackup) {
			t.Errorf("Open(%q) err = %v, want ErrNotBackup", in, err)
		}
		if msg := err.Error(); strings.Contains(msg, "passphrase") || strings.Contains(msg, "口令") {
			t.Errorf("Open(%q) message mentions the password: %q", in, msg)
		}
	}
}

func TestNewerVersionRejected(t *testing.T) {
	data := []byte(`{"format":"magpie-backup","version":2,"kdf":"future","iterations":"many"}` + "\nAAAA\n")
	_, err := Open(data, "pw")
	if !errors.Is(err, ErrNewer) {
		t.Fatalf("version 2: err = %v, want ErrNewer", err)
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatal("newer-version error reported as a password error")
	}
}

func TestCollectNoKeys(t *testing.T) {
	isolate(t)
	if err := provider.WriteStored([]provider.Provider{{
		ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-main", KeyName: "Personal",
		Keys:    []provider.KeyAccount{{Name: "Team", Key: "sk-team"}},
		Headers: map[string]string{"X-Api-Key": "abc", "X-Team": "blue", "Authorization": "Bearer x", "Cookie": "c"},
	}}); err != nil {
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
		t.Errorf("keys leaked with keys=false: %+v", p)
	}
	if len(p.Headers) != 1 || p.Headers["X-Team"] != "blue" {
		t.Errorf("headers with keys=false = %v, want only X-Team", p.Headers)
	}

	b, err = Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	p = b.Providers[0]
	if p.Key != "sk-main" || len(p.Keys) != 1 || len(p.Headers) != 4 {
		t.Errorf("keys=true lost data: %+v", p)
	}
}

func TestRestoreKeepsLocalKey(t *testing.T) {
	isolate(t)
	if err := provider.WriteStored([]provider.Provider{{
		ID: "relay", Name: "Old", Chat: "https://old.example/v1", Key: "sk-local",
		Keys: []provider.KeyAccount{{Name: "Spare", Key: "sk-spare"}},
	}}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "relay", Name: "New", Chat: "https://new.example/v1"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 1 || res.Added != 0 {
		t.Errorf("counts: added %d replaced %d", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 0 {
		t.Errorf("NeedKey = %v, want none: local key kept", res.NeedKey)
	}
	got := provider.Stored()
	if len(got) != 1 {
		t.Fatalf("stored %d providers", len(got))
	}
	p := got[0]
	if p.Key != "sk-local" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-spare" {
		t.Errorf("local keys not kept: %+v", p)
	}
	if p.Chat != "https://new.example/v1" || p.Name != "New" {
		t.Errorf("other fields not updated: %+v", p)
	}
}

func TestRestoreNeedKeyUsesName(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "fresh", Name: "Fresh Relay", Chat: "https://fresh.example/v1"},
		{ID: "withkey", Name: "With Key", Chat: "https://wk.example/v1", Key: "sk-x"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Errorf("added %d, want 2", res.Added)
	}
	if len(res.NeedKey) != 1 || res.NeedKey[0] != "Fresh Relay" {
		t.Errorf("NeedKey = %v, want [Fresh Relay]", res.NeedKey)
	}
}

func TestRestoreIgnoresInvalidIDs(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "Bad ID", Chat: "https://a.example/v1", Key: "k"},
		{ID: "magpie", Chat: "https://b.example/v1", Key: "k"},
		{ID: "", Chat: "https://c.example/v1", Key: "k"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Replaced != 0 {
		t.Errorf("counts added %d replaced %d, want 0", res.Added, res.Replaced)
	}
	if n := len(provider.Stored()); n != 0 {
		t.Errorf("%d providers stored, want 0", n)
	}
}

func TestRestoreManualBundleProvidersOnly(t *testing.T) {
	home := isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Settings || res.Profiles != 0 || res.Agents != 0 {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "magpie", "settings.json")); err == nil {
		t.Error("settings written though the bundle had none")
	}
}

func TestRestorePartsNotSelectedUntouched(t *testing.T) {
	home := isolate(t)
	s := settings.Settings{Lang: "en"}
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k"}},
		Settings:  &s,
		Profiles:  map[string]profile.Profile{"work": {"claude.model": "m"}},
		Agents:    map[string]string{"claude.model": "sonnet"},
	}
	res, err := Restore(b, Parts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 || res.Settings || res.Profiles != 0 || res.Agents != 0 || len(res.Skipped) != 0 {
		t.Errorf("result = %+v, want zero", res)
	}
	if n := len(provider.Stored()); n != 0 {
		t.Errorf("providers written with no part selected: %d", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "magpie", "settings.json")); err == nil {
		t.Error("settings written with no part selected")
	}
}

func TestRestoreSettingsAndProfiles(t *testing.T) {
	isolate(t)
	s := settings.Settings{Lang: "en", Theme: "dark"}
	b := Bundle{
		Version:  1,
		Settings: &s,
		Profiles: map[string]profile.Profile{"work": {"claude.model": "m"}},
	}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Settings || res.Profiles != 1 {
		t.Errorf("result = %+v", res)
	}
	if got := settings.Load(); got.Lang != "en" || got.Theme != "dark" {
		t.Errorf("settings = %+v", got)
	}
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	if ps["work"]["claude.model"] != "m" {
		t.Errorf("profiles = %v", ps)
	}
}

func TestRestoreAgentsSkipsUninstalled(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Agents: map[string]string{"gemini.model": "gemini-2.5-pro", "claude.model": "sonnet"}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 0 {
		t.Errorf("Agents = %d, want 0 with nothing installed", res.Agents)
	}
	joined := strings.Join(res.Skipped, "\n")
	if !strings.Contains(joined, "本机未安装") {
		t.Errorf("Skipped = %v, want the uninstalled agents listed", res.Skipped)
	}

	res, err = Restore(b, Parts{Providers: true, Settings: true, Profiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 0 || len(res.Skipped) != 0 {
		t.Errorf("agents touched with Agents=false: %+v", res)
	}
}

func TestRestoreIcons(t *testing.T) {
	isolate(t)
	png := []byte("\x89PNG\r\n\x1a\nicon-bytes-for-test")
	sum := sha256.Sum256(png)
	name := hex.EncodeToString(sum[:8]) + ".png"
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k", Icon: "file:" + name}},
		Icons:     map[string][]byte{name: png},
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(provider.IconFile(name))
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("icon not written: %v", err)
	}

	// a picture already on disk is not overwritten
	if err := os.WriteFile(provider.IconFile(name), []byte("local-copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(provider.IconFile(name)); string(got) != "local-copy" {
		t.Errorf("existing icon overwritten: %q", got)
	}
}

func TestRestoreDropsOversizedIcon(t *testing.T) {
	isolate(t)
	big := make([]byte, provider.MaxIcon+1)
	copy(big, "\x89PNG\r\n\x1a\n")
	sum := sha256.Sum256(big)
	name := hex.EncodeToString(sum[:8]) + ".png"
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k", Icon: "file:" + name}},
		Icons:     map[string][]byte{name: big},
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(provider.IconFile(name)); err == nil {
		t.Error("oversized icon written")
	}
	if got := provider.Stored(); got[0].Icon != "" {
		t.Errorf("icon reference kept for a dropped picture: %q", got[0].Icon)
	}
}

func TestRoundTripAcrossMachines(t *testing.T) {
	isolate(t)
	icon, err := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\nround-trip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.WriteStored([]provider.Provider{{
		ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-rt", Icon: icon,
		Headers: map[string]string{"X-Team": "t"},
	}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Settings{Lang: "zh"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("daily", profile.Profile{"claude.model": "sonnet"}); err != nil {
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

	isolate(t) // a fresh machine
	opened, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || !res.Settings || res.Profiles != 1 || len(res.NeedKey) != 0 {
		t.Errorf("result = %+v", res)
	}
	p := provider.Stored()[0]
	if p.Key != "sk-rt" || p.Headers["X-Team"] != "t" || p.Icon != icon {
		t.Errorf("provider = %+v", p)
	}
	if _, err := os.Stat(provider.IconFile(strings.TrimPrefix(icon, "file:"))); err != nil {
		t.Errorf("icon file missing after restore: %v", err)
	}
	if got := settings.Load(); got.Lang != "zh" {
		t.Errorf("settings = %+v", got)
	}
}
