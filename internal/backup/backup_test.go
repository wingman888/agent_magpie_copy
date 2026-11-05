package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// isolate points every magpie file and the home folder at a fresh temp dir,
// so tests never read or write the real configuration.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
}

func pngBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
}

// seedLocal writes a machine with a keyed provider, a credential header, a
// second key, an icon, settings and a profile.
func seedLocal(t *testing.T) {
	t.Helper()
	name, err := provider.StoreIcon(pngBytes())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.PutStored(provider.Provider{
		ID: "relay", Name: "My Relay", Icon: name,
		Key: "sk-secret-123", KeyName: "personal",
		Keys:      []provider.KeyAccount{{Name: "team", Key: "sk-team-456"}},
		Anthropic: "https://relay.example.com",
		Headers:   map[string]string{"X-Api-Key": "hdr-secret-789", "X-Team": "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("daily-work", profile.Profile{"claude.model": "opus"}); err != nil {
		t.Fatal(err)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || len(b.Providers) != 1 {
		t.Fatalf("collect: version %d, %d providers", b.Version, len(b.Providers))
	}
	data, err := Seal(b, "pw-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, "pw-1")
	if err != nil {
		t.Fatal(err)
	}
	p := got.Providers[0]
	if p.Key != "sk-secret-123" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-team-456" {
		t.Errorf("keys lost: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-secret-789" || p.Headers["X-Team"] != "blue" {
		t.Errorf("headers lost: %v", p.Headers)
	}
	if !bytes.Equal(got.Icons[strings.TrimPrefix(p.Icon, "file:")], pngBytes()) {
		t.Errorf("icon content lost")
	}
	if got.Settings == nil || got.Settings.Theme != "dark" {
		t.Errorf("settings lost: %+v", got.Settings)
	}
	if _, ok := got.Profiles["daily-work"]; !ok {
		t.Errorf("profile lost: %v", got.Profiles)
	}
}

func TestOpenWrongPassphrase(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, _ := Collect(true, "test")
	data, err := Seal(b, "right")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: got %v", err)
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestOpenTamperedIterationsRejected(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, _ := Collect(true, "test")
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("header does not carry iterations as \"iterations\": N")
	}
	tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	_, err = Open(tampered, "pw")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("tampered iterations: got %v", err)
	}
}

func TestOpenNotABackup(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"format":"other","version":1}`} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Fatalf("%q: no error", in)
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("%q: reported as passphrase error: %v", in, err)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%q: unexpected message %q", in, err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("%q: message mentions passphrase: %q", in, err)
		}
	}
}

func TestOpenNewerVersionAsksUpgrade(t *testing.T) {
	in := []byte(`{"format": "magpie-backup", "version": 2, "kdf": "future-kdf"}` + "\nAAAA\n")
	_, err := Open(in, "pw")
	if err == nil {
		t.Fatal("newer version accepted")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Errorf("newer version reported as passphrase error: %v", err)
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("message does not ask for an upgrade: %q", err)
	}
}

func TestSealedFileHasNoPlaintext(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, _ := Collect(true, "test")
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-secret-123", "sk-team-456", "hdr-secret-789", "relay.example.com", "My Relay", "daily-work", "personal"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("file contains %q in plain text", s)
		}
	}
}

func TestCollectNoKeysStripsCredentials(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := b.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Errorf("keys present in no-keys bundle: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Errorf("credential header kept: %v", p.Headers)
	}
	if p.Headers["X-Team"] != "blue" {
		t.Errorf("plain header dropped: %v", p.Headers)
	}
	if p.Name != "My Relay" || p.Anthropic != "https://relay.example.com" {
		t.Errorf("provider body changed: %+v", p)
	}
}

func TestCredentialHeaderNames(t *testing.T) {
	for _, n := range []string{"X-Api-Key", "Authorization", "X-Auth-Token", "Cookie", "X-Session-Id", "Client-Secret", "X-Password"} {
		if !credentialHeader(n) {
			t.Errorf("%s not treated as credential", n)
		}
	}
	for _, n := range []string{"X-Team", "User-Agent", "Anthropic-Version"} {
		if credentialHeader(n) {
			t.Errorf("%s treated as credential", n)
		}
	}
}

func TestRestoreKeepsLocalKeyAndCountsNeedKey(t *testing.T) {
	isolate(t)
	if _, err := provider.PutStored(provider.Provider{ID: "relay", Name: "Relay", Key: "local-key", KeyName: "mine", Anthropic: "https://old.example.com"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "Relay", Anthropic: "https://new.example.com"},
		{ID: "other", Name: "Other", Anthropic: "https://o.example.com"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 1 {
		t.Errorf("added %d replaced %d, want 1 and 1", res.Added, res.Replaced)
	}
	if !slices.Equal(res.NeedKey, []string{"Other"}) {
		t.Errorf("NeedKey = %v, want [Other]", res.NeedKey)
	}
	got := map[string]provider.Provider{}
	for _, p := range provider.StoredProviders() {
		got[p.ID] = p
	}
	if got["relay"].Key != "local-key" || got["relay"].KeyName != "mine" {
		t.Errorf("local key not kept: %+v", got["relay"])
	}
	if got["relay"].Anthropic != "https://new.example.com" {
		t.Errorf("provider body not updated: %+v", got["relay"])
	}
}

func TestRestoreIgnoresInvalidAndReservedIDs(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Key: "k", Anthropic: "https://a.example.com"},
		{ID: "Bad Id", Name: "Bad", Key: "k", Anthropic: "https://b.example.com"},
		{ID: "", Name: "Empty", Key: "k", Anthropic: "https://c.example.com"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 {
		t.Errorf("invalid ids counted: %+v", res)
	}
	if n := len(provider.StoredProviders()); n != 0 {
		t.Errorf("%d providers stored for invalid ids", n)
	}
}

func TestRestoreNoKeyBundleStoresNeedKeyProvider(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "fresh", Name: "Fresh", Anthropic: "https://f.example.com"},
	}}
	res, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || !slices.Equal(res.NeedKey, []string{"Fresh"}) {
		t.Errorf("result %+v", res)
	}
	if res.Settings || res.Profiles != 0 || res.Agents != 0 {
		t.Errorf("unselected parts touched: %+v", res)
	}
}

func TestRestoreNilSettingsLeavesLocalSettings(t *testing.T) {
	isolate(t)
	if err := settings.Save(settings.Settings{Theme: "light", Lang: "zh", Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings {
		t.Error("settings reported restored")
	}
	if s := settings.Load(); s.Theme != "light" || s.Lang != "zh" {
		t.Errorf("local settings overwritten: %+v", s)
	}
}

func TestRestoreSkipsUnselectedParts(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "x", Name: "X", Key: "k", Anthropic: "https://x.example.com"}},
		Settings:  &settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"},
		Profiles:  map[string]profile.Profile{"p": {"claude.model": "m"}},
		Agents:    profile.Profile{"nosuchagent.model": "m"},
	}
	res, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Settings || res.Profiles != 0 || res.Agents != 0 || len(res.Skipped) != 0 {
		t.Errorf("result %+v", res)
	}
	if len(profile.Names(mustLoadProfiles(t))) != 0 {
		t.Error("profiles written although not selected")
	}
}

func TestRestoreAgentUnknownSkipped(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Agents: profile.Profile{"nosuchagent.model": "m"}}
	res, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "nosuchagent.model") {
		t.Errorf("result %+v", res)
	}
}

func TestIconRestoredOnceAndKeptWhenPresent(t *testing.T) {
	isolate(t)
	seedLocal(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(b.Providers[0].Icon, "file:")
	if err := os.RemoveAll(provider.IconDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(provider.IconFile(name)); err != nil {
		t.Fatalf("icon not written: %v", err)
	}
	// a second restore finds the picture and does not touch it
	before, _ := os.ReadFile(provider.IconFile(name))
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(provider.IconFile(name))
	if !bytes.Equal(before, after) {
		t.Error("existing icon overwritten")
	}
}

func TestRestoreDropsOversizedIcon(t *testing.T) {
	isolate(t)
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...)
	b := Bundle{Version: 1, Icons: map[string][]byte{"0123456789abcdef.png": big},
		Providers: []provider.Provider{{ID: "x", Name: "X", Key: "k", Anthropic: "https://x.example.com", Icon: "file:0123456789abcdef.png"}}}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	ps := provider.StoredProviders()
	if len(ps) != 1 || ps[0].Icon != "" {
		t.Errorf("oversized icon kept: %+v", ps)
	}
}

func TestBundleWithoutSettingsOrProfilesRestoresProvidersOnly(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "y", Name: "Y", Key: "k", Anthropic: "https://y.example.com"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Settings || res.Profiles != 0 {
		t.Errorf("result %+v", res)
	}
}

func mustLoadProfiles(t *testing.T) map[string]profile.Profile {
	t.Helper()
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	return ps
}
