package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// machine points every config location at a fresh temp dir, so each call is
// a machine with nothing on it yet.
func machine(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
}

func findProvider(t *testing.T, id string) provider.Provider {
	t.Helper()
	for _, p := range provider.All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q not found", id)
	return provider.Provider{}
}

func TestRestoreManualVersionOneBundle(t *testing.T) {
	machine(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "manual", Name: "Manual", Key: "sk-manual-1", Chat: "https://manual.example.test/v1",
	}}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v, want Added 1, nothing else", r)
	}
	if r.Settings || r.Profiles != 0 || r.Agents != 0 {
		t.Errorf("empty parts changed: %+v", r)
	}
	if p := findProvider(t, "manual"); p.Key != "sk-manual-1" {
		t.Errorf("key = %q, want sk-manual-1", p.Key)
	}
}

func TestRestoreKeepsLocalKey(t *testing.T) {
	machine(t)
	if err := provider.Save(provider.Provider{ID: "vendor", Name: "Vendor", Key: "sk-local-KEEP", Chat: "https://old.example.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("vendor", "team", "sk-local-TEAM", ""); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "vendor", Name: "Vendor Renamed", Chat: "https://new.example.test/v1",
	}}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Replaced != 1 || r.Added != 0 {
		t.Errorf("result = %+v, want Replaced 1", r)
	}
	if len(r.NeedKey) != 0 {
		t.Errorf("NeedKey = %v, want none: local keys were kept", r.NeedKey)
	}
	p := findProvider(t, "vendor")
	if p.Key != "sk-local-KEEP" || p.Name != "Vendor Renamed" || p.Chat != "https://new.example.test/v1" {
		t.Errorf("provider after restore = %+v", p)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-local-TEAM" {
		t.Errorf("extra local key lost: %+v", p.Keys)
	}
}

func TestRestoreNeedKeyUsesName(t *testing.T) {
	machine(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "nokey", Name: "No Key Vendor", Chat: "https://nokey.example.test/v1",
	}}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 {
		t.Errorf("Added = %d, want 1", r.Added)
	}
	if !reflect.DeepEqual(r.NeedKey, []string{"No Key Vendor"}) {
		t.Errorf("NeedKey = %v, want the provider's Name", r.NeedKey)
	}
}

func TestRestoreIgnoresReservedAndInvalidIDs(t *testing.T) {
	machine(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Key: "sk-x", Chat: "https://a.example.test/v1"},
		{ID: "Bad Id!", Name: "Bad", Key: "sk-y", Chat: "https://b.example.test/v1"},
	}}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v, want nothing counted", r)
	}
	if n := len(provider.All()); n != 0 {
		t.Errorf("%d providers stored, want 0", n)
	}
}

func TestRestoreRespectsParts(t *testing.T) {
	machine(t)
	b := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "p1", Name: "P1", Key: "sk-p1", Chat: "https://p1.example.test/v1"}},
		Settings:  &settings.Settings{Theme: "dark"},
		Profiles:  map[string]profile.Profile{"work": {"claude.model": "m"}},
		Agents:    profile.Profile{"claude.model": "m"},
	}
	r, err := Restore(b, Parts{Providers: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 {
		t.Errorf("Added = %d, want 1", r.Added)
	}
	if r.Settings || r.Profiles != 0 || r.Agents != 0 || len(r.Skipped) != 0 {
		t.Errorf("unselected parts were touched: %+v", r)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		t.Error("settings file written although Settings was not selected")
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Errorf("profiles written although Profiles was not selected: %v", ps)
	}
}

func TestRestoreWithoutSettingsKeepsLocal(t *testing.T) {
	machine(t)
	if err := settings.Save(settings.Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Settings {
		t.Error("Settings = true for a bundle without settings")
	}
	if got := settings.Load(); got.Theme != "light" {
		t.Errorf("local settings changed: %+v", got)
	}
}

func TestRestoreSkipsUninstalledAgents(t *testing.T) {
	machine(t)
	b := Bundle{Version: 1, Agents: profile.Profile{"claude.model": "some-model"}}
	r, err := Restore(b, Parts{Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 0 {
		t.Errorf("Agents = %d, want 0", r.Agents)
	}
	if len(r.Skipped) != 1 || !strings.Contains(r.Skipped[0], "claude.model") || !strings.Contains(r.Skipped[0], "未安装") {
		t.Errorf("Skipped = %v, want one entry saying the agent is not installed", r.Skipped)
	}
}

func TestCollectNoKeysStripsCredentials(t *testing.T) {
	machine(t)
	err := provider.Save(provider.Provider{
		ID: "vendor", Name: "Vendor", Key: "sk-COLLECT-SECRET-1", Chat: "https://collect.example.test/v1",
		Headers: map[string]string{"X-Api-Key": "hdr-COLLECT-SECRET", "X-Team": "platform"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("vendor", "second", "sk-COLLECT-SECOND", ""); err != nil {
		t.Fatal(err)
	}

	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || b.App != "test" {
		t.Errorf("Version/App = %d/%q", b.Version, b.App)
	}
	if len(b.Providers) != 1 {
		t.Fatalf("Providers = %d, want 1", len(b.Providers))
	}
	p := b.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Errorf("keys survived keys=false: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Error("credential header survived keys=false")
	}
	if p.Headers["X-Team"] != "platform" {
		t.Errorf("ordinary header lost: %v", p.Headers)
	}
	raw, _ := json.Marshal(b)
	for _, secret := range []string{"sk-COLLECT-SECRET-1", "sk-COLLECT-SECOND", "hdr-COLLECT-SECRET"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("bundle still contains %q", secret)
		}
	}

	full, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if full.Providers[0].Key != "sk-COLLECT-SECRET-1" || len(full.Providers[0].Keys) != 1 {
		t.Errorf("keys=true lost keys: %+v", full.Providers[0])
	}
}

func TestCollectRestoreRoundTrip(t *testing.T) {
	machine(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{
		ID: "vendor", Name: "Vendor", Key: "sk-RT-KEY", Chat: "https://rt.example.test/v1",
		Icon:    icon,
		Headers: map[string]string{"X-Team": "platform"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work", profile.Profile{"claude.model": "m1"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Seal(b, testPass)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(data, testPass)
	if err != nil {
		t.Fatal(err)
	}

	// a fresh machine
	machine(t)
	r, err := Restore(opened, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Errorf("result = %+v", r)
	}
	if !r.Settings || r.Profiles != 1 {
		t.Errorf("settings/profiles not restored: %+v", r)
	}
	p := findProvider(t, "vendor")
	if p.Key != "sk-RT-KEY" || p.Chat != "https://rt.example.test/v1" || p.Headers["X-Team"] != "platform" {
		t.Errorf("provider = %+v", p)
	}
	if got, err := os.ReadFile(provider.IconFile(strings.TrimPrefix(p.Icon, "file:"))); err != nil || !bytes.Equal(got, png) {
		t.Errorf("icon not restored: %v", err)
	}
	if got := settings.Load(); got.Theme != "dark" {
		t.Errorf("settings = %+v", got)
	}
	if ps, _ := profile.Load(); ps["work"]["claude.model"] != "m1" {
		t.Errorf("profiles = %v", ps)
	}
}

func TestRestoreKeepsExistingIcon(t *testing.T) {
	machine(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(icon, "file:")
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "vendor", Name: "V", Key: "sk-i", Chat: "https://i.example.test/v1", Icon: icon}},
		Icons:     map[string][]byte{name: []byte("different bytes")},
	}
	if _, err := Restore(b, Parts{Providers: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(provider.IconFile(name)); !bytes.Equal(got, png) {
		t.Error("existing icon was overwritten")
	}
}
