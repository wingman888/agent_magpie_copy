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

func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
}

const png = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func setup(t *testing.T) {
	t.Helper()
	icon, err := provider.StoreIcon([]byte(png))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Icon: icon, Key: "sk-acme-secret",
		Keys:    []provider.KeyAccount{{Name: "second", Key: "sk-second-secret"}},
		Chat:    "https://api.acme-relay.example/v1",
		Headers: map[string]string{"X-Team": "blue-team-7", "X-Api-Key": "hdr-secret-9"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "beta", Name: "Beta", Key: "sk-beta-secret", Chat: "https://beta.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work-laptop-x", profile.Profile{"codex.model": "acme/m1"}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	setup(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sk-acme-secret", "sk-second-secret", "blue-team-7", "hdr-secret-9", "acme-relay", "work-laptop-x", "Acme"} {
		if bytes.Contains(sealed, []byte(s)) {
			t.Errorf("%q in the sealed file", s)
		}
	}
	if !strings.Contains(string(sealed), `"iterations": 600000`) {
		t.Error("no iterations line")
	}
	if _, err := Open(sealed, "bad"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	// iterations tampered
	tam := bytes.Replace(sealed, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	if _, err := Open(tam, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("tampered: %v", err)
	}
	got, err := Open(sealed, "pw")
	if err != nil {
		t.Fatal(err)
	}

	isolate(t) // a fresh machine
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || !r.Settings || r.Profiles != 1 || len(r.NeedKey) != 0 || r.Agents != 0 {
		t.Fatalf("result = %+v", r)
	}
	p, err := provider.Find("acme")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-acme-secret" || len(p.Keys) != 1 || p.Headers["X-Api-Key"] != "hdr-secret-9" {
		t.Errorf("acme = %+v", p)
	}
	if f := provider.IconFile(strings.TrimPrefix(p.Icon, "file:")); f == "" {
		t.Error("no icon file name")
	} else if d, err := os.ReadFile(f); err != nil || string(d) != png {
		t.Errorf("icon: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Errorf("settings = %+v", s)
	}
	if ps, _ := profile.Load(); ps["work-laptop-x"]["codex.model"] != "acme/m1" {
		t.Errorf("profiles = %v", ps)
	}
}

func TestNoKeys(t *testing.T) {
	isolate(t)
	setup(t)
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" || p.KeyProtocol != "" {
			t.Errorf("%s keeps a key", p.ID)
		}
		if _, bad := p.Headers["X-Api-Key"]; bad {
			t.Error("X-Api-Key kept")
		}
		if p.ID == "acme" && p.Headers["X-Team"] != "blue-team-7" {
			t.Error("X-Team lost")
		}
	}
	if len(b.Icons) != 1 {
		t.Errorf("icons = %d", len(b.Icons))
	}

	isolate(t)
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Key: "sk-here", Chat: "https://old.example/v1"}); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 1 || len(r.NeedKey) != 1 || r.NeedKey[0] != "Beta" || r.Settings || r.Profiles != 0 {
		t.Fatalf("result = %+v", r)
	}
	p, _ := provider.Find("acme")
	if p.Key != "sk-here" || p.Chat != "https://api.acme-relay.example/v1" || p.Headers["X-Team"] != "blue-team-7" {
		t.Errorf("acme = %+v", p)
	}
}

func TestOpenErrors(t *testing.T) {
	for _, in := range []string{"", "{}", `{"format":"magpie-backup","version":9,"kdf":"x"}`} {
		_, err := Open([]byte(in), "pw")
		if err == nil || errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q) = %v", in, err)
		}
	}
	if _, err := Open([]byte(`{"format":"magpie-backup","version":9,"kdf":"x"}`), "pw"); err == nil || !strings.Contains(err.Error(), "newer magpie") {
		t.Errorf("version: %v", err)
	}
	if _, err := Seal(Bundle{}, ""); err == nil {
		t.Error("empty passphrase sealed")
	}
}

func TestRestoreSkipsAgents(t *testing.T) {
	isolate(t)
	b := Bundle{Agents: profile.Profile{"codex.model": "x/y", "codex.provider": "x"}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 0 || len(r.Skipped) != 2 {
		t.Errorf("result = %+v", r)
	}
}

func TestRestoreIgnoresBadIDs(t *testing.T) {
	isolate(t)
	var ps []provider.Provider
	for _, id := range []string{"magpie", "", "Not A Slug"} {
		ps = append(ps, provider.Provider{ID: id, Name: "x", Key: "k", Chat: "https://x.example/v1"})
	}
	r, err := Restore(Bundle{Providers: ps}, All)
	if err != nil || r.Added != 0 || r.Replaced != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(provider.Path()); err == nil {
		t.Error("providers.json written")
	}
	_ = json.Marshal
}
