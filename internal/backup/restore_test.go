package backup

import (
	"encoding/json"
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

func storedProvider(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatalf("provider %q: %v", id, err)
	}
	return *p
}

func requireNoProvider(t *testing.T, id string) {
	t.Helper()
	if _, err := provider.Find(id); err == nil {
		t.Fatalf("provider %q exists, want it absent", id)
	}
}

func TestRestoreKeepsLocalKeyForKeylessProvider(t *testing.T) {
	sandbox(t)
	mustSaveProvider(t, provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://old.acme.test/v1",
		Key: "sk-LOCAL", KeyName: "mine",
		Keys: []provider.KeyAccount{{Name: "spare", Key: "sk-LOCAL-2"}},
	})

	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "acme", Name: "Acme Renamed", Chat: "https://new.acme.test/v1",
		Headers: map[string]string{"X-Team": "blue"},
	}}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Replaced != 1 || r.Added != 0 {
		t.Fatalf("Added=%d Replaced=%d, want 0 and 1", r.Added, r.Replaced)
	}
	if len(r.NeedKey) != 0 {
		t.Fatalf("NeedKey=%v, want none: the local key is kept", r.NeedKey)
	}
	got := storedProvider(t, "acme")
	if got.Key != "sk-LOCAL" || got.KeyName != "mine" || len(got.Keys) != 1 || got.Keys[0].Key != "sk-LOCAL-2" {
		t.Fatalf("local keys not kept: %+v", got)
	}
	if got.Name != "Acme Renamed" || got.Chat != "https://new.acme.test/v1" || got.Headers["X-Team"] != "blue" {
		t.Fatalf("other fields not updated: %+v", got)
	}
}

func TestRestoreNewKeylessProviderNeedsKey(t *testing.T) {
	sandbox(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "fresh", Name: "Fresh Co", Chat: "https://fresh.test/v1",
	}}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 {
		t.Fatalf("Added=%d Replaced=%d, want 1 and 0", r.Added, r.Replaced)
	}
	if !reflect.DeepEqual(r.NeedKey, []string{"Fresh Co"}) {
		t.Fatalf("NeedKey=%v, want [Fresh Co] (the Name, not the id)", r.NeedKey)
	}
	if got := storedProvider(t, "fresh"); got.Name != "Fresh Co" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestRestoreIgnoresReservedAndInvalidIDs(t *testing.T) {
	sandbox(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Chat: "https://m.test/v1", Key: "k"},
		{ID: "Bad ID!", Name: "Bad", Chat: "https://bad.test/v1", Key: "k"},
		{ID: "", Name: "Empty", Chat: "https://empty.test/v1", Key: "k"},
		{ID: "ok-one", Name: "Ok", Chat: "https://ok.test/v1", Key: "k"},
	}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Fatalf("Added=%d Replaced=%d NeedKey=%v, want 1, 0, none", r.Added, r.Replaced, r.NeedKey)
	}
	requireNoProvider(t, "magpie")
	requireNoProvider(t, "bad-id")
	storedProvider(t, "ok-one")
}

func TestRestoreUnselectedPartsStayZeroAndUntouched(t *testing.T) {
	sandbox(t)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "x-new", Name: "X", Chat: "https://x.test/v1", Key: "k"}},
		Settings:  json.RawMessage(`{"theme":"light"}`),
		Profiles:  json.RawMessage(`{"day":{"claude.model":"m"}}`),
		Agents:    json.RawMessage(`{"claude":{"model":"m"}}`),
	}
	r, err := Restore(b, Parts{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Replaced != 0 || len(r.NeedKey) != 0 || r.Settings || r.Profiles != 0 || r.Agents != 0 || len(r.Skipped) != 0 {
		t.Fatalf("empty Parts produced %+v", r)
	}
	requireNoProvider(t, "x-new")
	if got := settings.Load().Theme; got != "dark" {
		t.Fatalf("settings theme = %q, want dark", got)
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Fatalf("profiles written with Parts{}: %v", ps)
	}
}

func TestRestoreProvidersOnlyLeavesSettingsAndProfiles(t *testing.T) {
	sandbox(t)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "x-new", Name: "X", Chat: "https://x.test/v1", Key: "k"}},
		Settings:  json.RawMessage(`{"theme":"light"}`),
		Profiles:  json.RawMessage(`{"day":{"claude.model":"m"}}`),
	}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings || r.Profiles != 0 || r.Added != 1 {
		t.Fatalf("result %+v, want Added=1 and no settings or profiles", r)
	}
	if got := settings.Load().Theme; got != "dark" {
		t.Fatalf("settings theme = %q, want dark", got)
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Fatalf("profiles written though not selected: %v", ps)
	}
}

func TestRestoreEmptySettingsDoNotOverwrite(t *testing.T) {
	sandbox(t)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings {
		t.Fatal("Settings reported restored for a bundle without settings")
	}
	if got := settings.Load().Theme; got != "dark" {
		t.Fatalf("settings theme = %q, want dark", got)
	}
}

func TestRestoreIconsNotOverwritten(t *testing.T) {
	sandbox(t)
	existing := "0123456789abcdef.png"
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.IconFile(existing), []byte("LOCAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := "aaaaaaaaaaaaaaaa.png"
	oversized := "fedcba9876543210.png"

	b := Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID: "pic", Name: "Pic", Chat: "https://pic.test/v1", Key: "k", Icon: "file:" + existing,
		}},
		Icons: map[string][]byte{
			existing:  []byte("BUNDLE"),
			fresh:     []byte("NEW"),
			oversized: make([]byte, provider.MaxIcon+1),
		},
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(provider.IconFile(existing)); string(got) != "LOCAL" {
		t.Fatalf("existing icon overwritten: %q", got)
	}
	if got, _ := os.ReadFile(provider.IconFile(fresh)); string(got) != "NEW" {
		t.Fatalf("new icon not written: %q", got)
	}
	if _, err := os.Stat(provider.IconFile(oversized)); err == nil {
		t.Fatal("oversized icon was written")
	}
}

func TestRestoreSkipsUninstalledAgent(t *testing.T) {
	home := sandbox(t)
	// No ~/.codex here, so Codex is not installed on this machine.
	b := Bundle{Version: 1, Agents: json.RawMessage(`{"codex":{"model":"gpt-x"}}`)}
	r, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 0 {
		t.Fatalf("Agents=%d, want 0", r.Agents)
	}
	want := "Codex：本机未安装该 Agent"
	if !containsString(r.Skipped, want) {
		t.Fatalf("Skipped=%v, want it to contain %q", r.Skipped, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		t.Fatal("restore created the agent's config directory")
	}
}

func TestRestoreAppliesInstalledAgent(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Agents: json.RawMessage(`{"claude":{"model":"claude-test-model"}}`)}
	r, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 1 {
		t.Fatalf("Agents=%d, want 1; Skipped=%v", r.Agents, r.Skipped)
	}
	if got, _ := edit.GetJSON(path, "model"); got != "claude-test-model" {
		t.Fatalf("claude model = %q", got)
	}
	if got, _ := edit.GetJSON(path, "theme"); got != "dark" {
		t.Fatalf("unrelated setting changed: theme = %q", got)
	}
}

func TestRestoreRoundTripAcrossMachines(t *testing.T) {
	sandbox(t)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("y", 64))
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveProvider(t, provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1", Key: "sk-ROUND-1",
		Icon: icon, Headers: map[string]string{"X-Api-Key": "HDR", "X-Team": "t"},
	})
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("day", profile.Profile{"claude.model": "acme/m"}); err != nil {
		t.Fatal(err)
	}
	src, err := Collect(true, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(src, "pw-123")
	if err != nil {
		t.Fatal(err)
	}

	// A fresh machine.
	sandbox(t)
	b, err := Open(sealed, "pw-123")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 || !r.Settings || r.Profiles != 1 {
		t.Fatalf("restore result %+v", r)
	}
	got := storedProvider(t, "acme")
	if got.Key != "sk-ROUND-1" || got.Headers["X-Api-Key"] != "HDR" || got.Headers["X-Team"] != "t" {
		t.Fatalf("provider not restored: %+v", got)
	}
	name := strings.TrimPrefix(icon, "file:")
	if data, err := os.ReadFile(provider.IconFile(name)); err != nil || string(data) != string(png) {
		t.Fatalf("icon not restored: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "en" {
		t.Fatalf("settings = %+v", s)
	}
	if ps, _ := profile.Load(); ps["day"]["claude.model"] != "acme/m" {
		t.Fatalf("profiles = %v", ps)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}
