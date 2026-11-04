package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// isolate points magpie's config and the home folder at a fresh temp dir, so
// the test never reads or writes this machine's configuration.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func sampleProvider() provider.Provider {
	return provider.Provider{
		ID:        "relay",
		Name:      "Relay Co",
		Key:       "sk-secret-123456789",
		KeyName:   "Personal",
		Keys:      []provider.KeyAccount{{Name: "Team", Key: "sk-team-987654321"}},
		Anthropic: "https://relay.example.com",
		Headers:   map[string]string{"X-Api-Key": "hdr-secret-value", "X-Team": "blue"},
	}
}

func mustImport(t *testing.T, ps ...provider.Provider) {
	t.Helper()
	if _, err := provider.Import(ps); err != nil {
		t.Fatal(err)
	}
}

func seal(t *testing.T, b Bundle, pass string) []byte {
	t.Helper()
	data, err := Seal(b, pass)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// edited re-encodes a sealed file after changing one of its top-level fields.
func edited(t *testing.T, data []byte, change func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSealOpenRoundTrip(t *testing.T) {
	isolate(t)
	mustImport(t, sampleProvider())
	if err := profile.Save("workday-profile", profile.Profile{"claude.model": "opus-x"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 {
		t.Fatalf("Collect Version = %d, want 1", b.Version)
	}
	data := seal(t, b, "pw")
	for _, secret := range []string{"sk-secret-123456789", "sk-team-987654321", "hdr-secret-value", "relay.example.com", "workday-profile", "Relay Co"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("sealed file contains plaintext %q", secret)
		}
	}
	got, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(got.Providers))
	}
	p := got.Providers[0]
	if p.Key != "sk-secret-123456789" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-team-987654321" {
		t.Errorf("keys did not round-trip: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-secret-value" || p.Headers["X-Team"] != "blue" {
		t.Errorf("headers did not round-trip: %v", p.Headers)
	}
	if got.Profiles["workday-profile"]["claude.model"] != "opus-x" {
		t.Errorf("profiles did not round-trip: %v", got.Profiles)
	}
}

func TestOpenWrongPassphrase(t *testing.T) {
	isolate(t)
	data := seal(t, Bundle{Version: 1}, "right")
	_, err := Open(data, "wrong")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: err = %v, want ErrPassphrase", err)
	}
}

func TestSealEmptyPassphrase(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("Seal with empty passphrase succeeded")
	}
}

func TestOpenRejectsModifiedHeader(t *testing.T) {
	isolate(t)
	mustImport(t, sampleProvider())
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data := seal(t, b, "pw")

	cases := map[string]func(m map[string]any){
		"iterations lowered to 100000": func(m map[string]any) { m["iterations"] = float64(100000) },
		"iterations changed":           func(m map[string]any) { m["iterations"] = float64(600001) },
		"kdf changed":                  func(m map[string]any) { m["kdf"] = "pbkdf2-sha512" },
		"nonce replaced":               func(m map[string]any) { m["nonce"] = "AAAAAAAAAAAAAAAA" },
		"field added":                  func(m map[string]any) { m["extra"] = "x" },
		"version zeroed":               func(m map[string]any) { m["version"] = float64(0) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(edited(t, data, change), "pw")
			if !errors.Is(err, ErrPassphrase) {
				t.Fatalf("err = %v, want ErrPassphrase", err)
			}
		})
	}
}

func TestOpenNotABackup(t *testing.T) {
	for _, in := range []string{"", "   ", "hello", "{}", "[]"} {
		_, err := Open([]byte(in), "pw")
		if err == nil {
			t.Fatalf("Open(%q) succeeded", in)
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q): got ErrPassphrase, want a not-a-backup error", in)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("Open(%q): err = %q, want the not-a-backup message", in, err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("Open(%q): format error mentions passphrase: %q", in, err)
		}
	}
}

func TestOpenNewerVersion(t *testing.T) {
	isolate(t)
	data := seal(t, Bundle{Version: 1}, "pw")
	newer := edited(t, data, func(m map[string]any) { m["version"] = float64(2) })
	_, err := Open(newer, "pw")
	if err == nil {
		t.Fatal("version 2 opened")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatalf("version 2: got ErrPassphrase, want an upgrade error")
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("version 2: err = %q, want the upgrade message", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Errorf("upgrade error mentions passphrase: %q", err)
	}
}

func TestSealDefaultIterations(t *testing.T) {
	data := seal(t, Bundle{Version: 1}, "pw")
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	it, _ := m["iterations"].(float64)
	if it <= 100000 {
		t.Fatalf("iterations = %v, want > 100000", m["iterations"])
	}
	if !bytes.Contains(data, []byte(`"iterations": `)) {
		t.Errorf("header does not spell iterations as \"iterations\": N")
	}
}

func TestCollectNoKeys(t *testing.T) {
	isolate(t)
	p := sampleProvider()
	p.Headers["Authorization"] = "Bearer x"
	p.Headers["X-Session-Id"] = "s"
	p.Headers["x-password"] = "p"
	mustImport(t, p)

	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(b.Providers))
	}
	got := b.Providers[0]
	if got.Key != "" || len(got.Keys) != 0 || got.KeyName != "" {
		t.Errorf("keys left in --no-keys bundle: %+v", got)
	}
	for _, h := range []string{"X-Api-Key", "Authorization", "X-Session-Id", "x-password"} {
		if _, ok := got.Headers[h]; ok {
			t.Errorf("credential header %q kept", h)
		}
	}
	if got.Headers["X-Team"] != "blue" {
		t.Errorf("plain header X-Team dropped: %v", got.Headers)
	}
	data := seal(t, b, "pw")
	if bytes.Contains(data, []byte("sk-secret")) || bytes.Contains(data, []byte("hdr-secret")) {
		t.Error("no-keys file contains a secret")
	}
}

func TestRestoreKeepsLocalKey(t *testing.T) {
	isolate(t)
	mustImport(t, sampleProvider())

	incoming := provider.Provider{ID: "relay", Name: "Relay Renamed", Anthropic: "https://new.example.com"}
	res, err := Restore(Bundle{Version: 1, Providers: []provider.Provider{incoming}}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced != 1 || res.Added != 0 {
		t.Errorf("Added/Replaced = %d/%d, want 0/1", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 0 {
		t.Errorf("NeedKey = %v, want none: the local key was kept", res.NeedKey)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-secret-123456789" || len(p.Keys) != 1 || p.KeyName != "Personal" {
		t.Errorf("local keys lost: %+v", p)
	}
	if p.Name != "Relay Renamed" || p.Anthropic != "https://new.example.com" {
		t.Errorf("other fields not updated: %+v", p)
	}
}

func TestRestoreCountsAndNeedKey(t *testing.T) {
	isolate(t)
	mustImport(t, sampleProvider())

	ps := []provider.Provider{
		{ID: "relay", Name: "Relay Co", Anthropic: "https://relay.example.com"}, // replaces; keeps local key
		{ID: "new-one", Name: "New One", Anthropic: "https://new.example.com"},  // added, no key
		{ID: "keyed", Name: "Keyed", Anthropic: "https://k.example.com", Key: "k-123456789"},
		{ID: "magpie", Name: "Gateway", Anthropic: "https://g.example.com", Key: "g"},
		{ID: "Bad_ID", Name: "Bad", Anthropic: "https://b.example.com", Key: "b"},
	}
	res, err := Restore(Bundle{Version: 1, Providers: ps}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Replaced != 1 {
		t.Errorf("Added/Replaced = %d/%d, want 2/1", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 1 || res.NeedKey[0] != "New One" {
		t.Errorf("NeedKey = %v, want [New One] (names, not ids)", res.NeedKey)
	}
	if _, err := provider.Find("magpie"); err == nil {
		t.Error("reserved id magpie was written")
	}
}

func TestRestoreSkipsUnselectedParts(t *testing.T) {
	isolate(t)
	if err := settings.Save(settings.Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "new-one", Name: "New One", Anthropic: "https://new.example.com"}},
		Settings:  &settings.Settings{Theme: "dark"},
		Profiles:  map[string]profile.Profile{"p": {"claude.model": "m"}},
		Agents:    profile.Profile{"claude.model": "m"},
	}
	res, err := Restore(b, Parts{Settings: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Settings || res.Profiles != 0 || res.Agents != 0 || res.Added != 0 {
		t.Errorf("unexpected result for settings-only restore: %+v", res)
	}
	if settings.Load().Theme != "dark" {
		t.Errorf("settings not restored")
	}
	if _, err := provider.Find("new-one"); err == nil {
		t.Error("provider written although Providers was not selected")
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Errorf("profiles written although Profiles was not selected: %v", ps)
	}
}

func TestRestoreManualBundleWithoutOptionalParts(t *testing.T) {
	isolate(t)
	res, err := Restore(Bundle{Version: 1, Providers: []provider.Provider{{ID: "relay", Name: "Relay", Anthropic: "https://r.example.com"}}}, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Settings || res.Profiles != 0 || res.Agents != 0 {
		t.Errorf("unexpected result: %+v", res)
	}
	if len(res.NeedKey) != 1 || res.NeedKey[0] != "Relay" {
		t.Errorf("NeedKey = %v, want [Relay]", res.NeedKey)
	}
}

func TestRestoreFreshMachine(t *testing.T) {
	isolate(t)
	png := []byte("\x89PNG\r\n\x1a\nfake-picture-bytes")
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	p := sampleProvider()
	p.Icon = icon
	mustImport(t, p)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("workday-profile", profile.Profile{"claude.model": "opus-x"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data := seal(t, b, "pw")

	// a second machine, with nothing on it
	isolate(t)
	got, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 0 || !res.Settings || res.Profiles != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(res.NeedKey) != 0 {
		t.Errorf("NeedKey = %v, want none", res.NeedKey)
	}
	restored, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Key != "sk-secret-123456789" || restored.Headers["X-Api-Key"] != "hdr-secret-value" {
		t.Errorf("provider not restored: %+v", restored)
	}
	name := strings.TrimPrefix(restored.Icon, "file:")
	if name == restored.Icon {
		t.Fatalf("icon not restored on provider: %q", restored.Icon)
	}
	if data, err := os.ReadFile(provider.IconFile(name)); err != nil || !bytes.Equal(data, png) {
		t.Errorf("icon file missing or different: %v", err)
	}
	if settings.Load().Theme != "dark" {
		t.Error("settings not restored")
	}
}

func TestRestoreRejectsNewerBundle(t *testing.T) {
	isolate(t)
	if _, err := Restore(Bundle{Version: 2}, All); err == nil {
		t.Fatal("Restore accepted a bundle newer than this magpie")
	}
}
