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

func isolate(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(d, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(d, "cache"))
	t.Setenv("PATH", "")
}

func sample() Bundle {
	return Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "relay", Name: "Secret Relay", Key: "sk-topsecret", Chat: "https://relay.example.org/v1",
		Keys:    []provider.KeyAccount{{Name: "b", Key: "sk-second"}},
		Headers: map[string]string{"X-Api-Key": "hdrsecret", "X-Team": "red", "Proxy-Authorization": "x"},
	}}}
}

func TestSealOpenRoundTrip(t *testing.T) {
	data, err := Seal(sample(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-topsecret", "relay.example.org", "Secret Relay", "hdrsecret", "red"} {
		if bytes.Contains(data, []byte(s)) {
			t.Fatalf("plaintext %q in file", s)
		}
	}
	if !strings.Contains(string(data), `"iterations": 600000`) {
		t.Fatalf("header: %s", data)
	}
	b, err := Open(data, "pw")
	if err != nil || b.Providers[0].Key != "sk-topsecret" || b.Providers[0].Keys[0].Key != "sk-second" {
		t.Fatalf("%v %+v", err, b)
	}
	if _, err := Open(data, "bad"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong pass: %v", err)
	}
	if _, err := Seal(sample(), ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}

func TestOpenTamper(t *testing.T) {
	data, _ := Seal(sample(), "pw")
	for _, to := range []string{"100000", "1000", "600001", "99999999"} {
		bad := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": `+to), 1)
		if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("iterations %s: %v", to, err)
		}
	}
	bad := bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 0`), 1)
	if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("version: %v", err)
	}
	bad = append([]byte{}, data...)
	bad[len(bad)-6] ^= 1
	if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("body: %v", err)
	}
}

func TestOpenFormat(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"format":"other","version":1}`} {
		_, err := Open([]byte(in), "pw")
		if !errors.Is(err, ErrNotBackup) || errors.Is(err, ErrPassphrase) {
			t.Fatalf("%q: %v", in, err)
		}
		if s := strings.ToLower(err.Error()); strings.Contains(s, "passphrase") || strings.Contains(s, "口令") {
			t.Fatalf("message: %v", err)
		}
	}
	_, err := Open([]byte(`{"format":"magpie-backup","version":2}`+"\nxx"), "pw")
	if !errors.Is(err, ErrTooNew) || errors.Is(err, ErrPassphrase) {
		t.Fatalf("%v", err)
	}
	if s := strings.ToLower(err.Error()); strings.Contains(s, "passphrase") || strings.Contains(s, "口令") {
		t.Fatalf("message: %v", err)
	}
}

func TestCollectNoKeys(t *testing.T) {
	isolate(t)
	p := sample().Providers[0]
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil || b.Version != 1 || len(b.Providers) != 1 {
		t.Fatalf("%v %+v", err, b)
	}
	got := b.Providers[0]
	if got.Key != "" || len(got.Keys) != 0 || got.KeyName != "" {
		t.Fatalf("keys left: %+v", got)
	}
	if len(got.Headers) != 1 || got.Headers["X-Team"] != "red" {
		t.Fatalf("headers: %v", got.Headers)
	}
	b, _ = Collect(true, "test")
	if b.Providers[0].Key != "sk-topsecret" || len(b.Providers[0].Headers) != 3 || b.Settings != nil {
		t.Fatalf("%+v", b.Providers[0])
	}
}

func TestRestoreRules(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Old", Key: "local-key", Chat: "https://old.example.org/v1",
		Keys: []provider.KeyAccount{{Key: "local-2"}}}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "New", Chat: "https://new.example.org/v1"},
		{ID: "fresh", Name: "Fresh One", Chat: "https://fresh.example.org/v1"},
		{ID: "magpie", Name: "Gw", Chat: "https://x.example.org/v1", Key: "k"},
		{ID: "Bad Id!", Name: "Bad", Chat: "https://y.example.org/v1", Key: "k"},
		{ID: "keyed", Name: "Keyed", Chat: "https://k.example.org/v1", Key: "k"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Replaced != 1 || r.Settings || r.Profiles != 0 || r.Agents != 0 {
		t.Fatalf("%+v", r)
	}
	if len(r.NeedKey) != 1 || r.NeedKey[0] != "Fresh One" {
		t.Fatalf("need key: %v", r.NeedKey)
	}
	p, err := provider.Find("relay")
	if err != nil || p.Name != "New" || p.Key != "local-key" || len(p.Keys) != 1 || p.Chat != "https://new.example.org/v1" {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := provider.Find("magpie"); err == nil {
		t.Fatal("reserved id restored")
	}
}

func TestRestoreParts(t *testing.T) {
	isolate(t)
	s := settings.Settings{Theme: "dark", Lang: "zh", Tray: "panel"}
	b := Bundle{Version: 1, Providers: sample().Providers, Settings: &s,
		Profiles: map[string]profile.Profile{"work": {"claude.model": "x"}, "home": {}}}
	r, err := Restore(b, Parts{Profiles: true})
	if err != nil || r.Profiles != 2 || r.Added != 0 || r.Settings {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		t.Fatal("settings written")
	}
	if len(provider.Stored()) != 0 {
		t.Fatal("providers written")
	}
	r, err = Restore(b, Parts{Settings: true})
	if err != nil || !r.Settings || settings.Load().Theme != "dark" {
		t.Fatalf("%v %+v", err, r)
	}
	r, _ = Restore(Bundle{Version: 1}, All)
	if r.Settings {
		t.Fatal("settings restored from none")
	}
}

func TestRestoreIcons(t *testing.T) {
	isolate(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	name, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	fn := strings.TrimPrefix(name, "file:")
	os.Remove(provider.IconFile(fn))
	b := Bundle{Version: 1, Icons: map[string][]byte{fn: png, "../evil.png": png}}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(provider.IconFile(fn)); err != nil {
		t.Fatal("icon not restored")
	}
	if _, err := os.Stat(filepath.Join(provider.IconDir(), "..", "evil.png")); err == nil {
		t.Fatal("path traversal")
	}
}
