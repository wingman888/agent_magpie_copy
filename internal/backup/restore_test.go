package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// isolate points every magpie file, the home folder and the PATH (so no agent
// counts as installed) at empty temporary folders. Nothing touches the real
// user directories.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("PATH", t.TempDir())
}

func mustSave(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatalf("provider.Save: %v", err)
	}
}

func findProvider(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatalf("Find(%q): %v", id, err)
	}
	return *p
}

func TestCollectNoKeysStripsCredentialsAndKeepsPlainHeaders(t *testing.T) {
	isolate(t)
	mustSave(t, provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.test/v1",
		Key: "sk-live-AAA", KeyName: "personal",
		Keys:    []provider.KeyAccount{{Name: "team", Key: "sk-live-BBB"}},
		Headers: map[string]string{"X-Api-Key": "hdr-AAA", "X-Team": "red", "X-Session-Id": "s-1"},
	})

	b, err := Collect(false, "test-app")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if b.Version != 1 || b.App != "test-app" {
		t.Fatalf("Version=%d App=%q", b.Version, b.App)
	}
	if len(b.Providers) != 1 {
		t.Fatalf("providers = %+v", b.Providers)
	}
	p := b.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Fatalf("keys leaked: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Fatal("credential header X-Api-Key kept")
	}
	if _, ok := p.Headers["X-Session-Id"]; ok {
		t.Fatal("credential header X-Session-Id kept")
	}
	if p.Headers["X-Team"] != "red" {
		t.Fatalf("plain header lost: %+v", p.Headers)
	}
	raw, _ := json.Marshal(b)
	for _, s := range []string{"sk-live", "hdr-AAA"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("bundle contains %q", s)
		}
	}

	full, err := Collect(true, "test-app")
	if err != nil {
		t.Fatalf("Collect(keys): %v", err)
	}
	fp := full.Providers[0]
	if fp.Key != "sk-live-AAA" || len(fp.Keys) != 1 || fp.Headers["X-Api-Key"] != "hdr-AAA" {
		t.Fatalf("keys=true dropped data: %+v", fp)
	}
}

func TestCollectEmptyMachine(t *testing.T) {
	isolate(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(b.Providers) != 0 || b.Settings != nil || b.Profiles != nil || b.Agents != nil {
		t.Fatalf("empty machine gave %+v", b)
	}
}

func TestRestoreKeepsLocalKeyWhenBackupHasNone(t *testing.T) {
	isolate(t)
	mustSave(t, provider.Provider{ID: "relay", Name: "Old", Chat: "https://old.example.test/v1", Key: "local-key"})

	b := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "relay", Name: "New Relay", Chat: "https://new.example.test/v1",
		Headers: map[string]string{"X-Team": "blue"},
	}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 0 || res.Replaced != 1 || len(res.NeedKey) != 0 {
		t.Fatalf("result = %+v", res)
	}
	got := findProvider(t, "relay")
	if got.Key != "local-key" {
		t.Fatalf("local key lost: %+v", got)
	}
	if got.Name != "New Relay" || got.Chat != "https://new.example.test/v1" || got.Headers["X-Team"] != "blue" {
		t.Fatalf("provider not updated: %+v", got)
	}
}

func TestRestoreNeedKeyUsesNameAndCountsOnlyWritten(t *testing.T) {
	isolate(t)
	mustSave(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.test/v1", Key: "local-key"})

	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "Relay", Chat: "https://relay.example.test/v1"},
		{ID: "fresh", Name: "Fresh Vendor", Chat: "https://fresh.example.test/v1"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || res.Replaced != 1 {
		t.Fatalf("Added=%d Replaced=%d", res.Added, res.Replaced)
	}
	if !reflect.DeepEqual(res.NeedKey, []string{"Fresh Vendor"}) {
		t.Fatalf("NeedKey = %v", res.NeedKey)
	}
}

func TestRestoreIgnoresReservedAndInvalidIDs(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Chat: "https://gw.example.test/v1", Key: "k"},
		{ID: "Bad ID!", Name: "Bad", Chat: "https://bad.example.test/v1", Key: "k"},
		{ID: "", Name: "Empty", Chat: "https://empty.example.test/v1", Key: "k"},
		{ID: "ok-one", Name: "Ok", Chat: "https://ok.example.test/v1", Key: "k"},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || res.Replaced != 0 || len(res.NeedKey) != 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, p := range provider.All() {
		if p.ID == "magpie" || strings.Contains(p.ID, " ") {
			t.Fatalf("reserved or invalid id written: %q", p.ID)
		}
	}
}

func TestRestoreVersionOneProvidersOnlyBundle(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "plain", Name: "Plain", Chat: "https://plain.example.test/v1", Key: "k"}}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || res.Settings || res.Profiles != 0 || res.Agents != 0 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(settings.Path()); !os.IsNotExist(err) {
		t.Fatal("settings were written from a bundle without settings")
	}
}

func TestRestorePartsNotSelectedLeftAlone(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "plain", Name: "Plain", Chat: "https://plain.example.test/v1", Key: "k"}}}
	res, err := Restore(b, Parts{})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !reflect.DeepEqual(res, Result{}) {
		t.Fatalf("result = %+v, want zero", res)
	}
	if _, err := provider.Find("plain"); err == nil {
		t.Fatal("provider written although Providers part not selected")
	}
}

func TestRestoreSettingsProfilesIconsRoundTrip(t *testing.T) {
	isolate(t)
	const icon = "0123456789abcdef.png"
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.IconFile(icon), []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work", profile.Profile{"claude.model": "opus"}); err != nil {
		t.Fatal(err)
	}
	mustSave(t, provider.Provider{ID: "pic", Name: "Pic", Chat: "https://pic.example.test/v1", Key: "k", Icon: "file:" + icon})

	b, err := Collect(true, "test")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if string(b.Icons[icon]) != "PNGDATA" || b.Settings == nil || b.Profiles == nil {
		t.Fatalf("bundle incomplete: icons=%d settings=%v profiles=%v", len(b.Icons), b.Settings != nil, b.Profiles != nil)
	}

	// A fresh machine.
	isolate(t)
	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || !res.Settings || res.Profiles != 1 {
		t.Fatalf("result = %+v", res)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("settings = %+v", s)
	}
	ps, err := profile.Load()
	if err != nil || ps["work"]["claude.model"] != "opus" {
		t.Fatalf("profiles = %v, %v", ps, err)
	}
	if got := findProvider(t, "pic"); got.Icon != "file:"+icon {
		t.Fatalf("icon = %q", got.Icon)
	}
	data, err := os.ReadFile(provider.IconFile(icon))
	if err != nil || string(data) != "PNGDATA" {
		t.Fatalf("icon file = %q, %v", data, err)
	}
}

func TestRestoreIconRules(t *testing.T) {
	isolate(t)
	const kept = "aaaaaaaaaaaaaaaa.png"
	const big = "bbbbbbbbbbbbbbbb.png"
	if err := os.MkdirAll(provider.IconDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.IconFile(kept), []byte("LOCAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1,
		Providers: []provider.Provider{
			{ID: "a", Name: "A", Chat: "https://a.example.test/v1", Key: "k", Icon: "file:" + kept},
			{ID: "b", Name: "B", Chat: "https://b.example.test/v1", Key: "k", Icon: "file:" + big},
		},
		Icons: map[string][]byte{
			kept: []byte("BACKUP"),
			big:  make([]byte, maxIcon+1),
		},
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, _ := os.ReadFile(provider.IconFile(kept)); string(data) != "LOCAL" {
		t.Fatalf("existing icon overwritten: %q", data)
	}
	if _, err := os.Stat(provider.IconFile(big)); !os.IsNotExist(err) {
		t.Fatal("oversized icon was written")
	}
}

func TestRestoreAgentsSkipsUninstalledAndHonoursNoAgents(t *testing.T) {
	isolate(t)
	b := Bundle{Version: 1, Agents: json.RawMessage(`{"claude.model":"opus","nosuch.model":"x"}`)}

	res, err := Restore(b, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Agents != 0 {
		t.Fatalf("Agents = %d", res.Agents)
	}
	want := []string{"claude.model：本机未安装该 Agent", "nosuch.model：本机不支持该 Agent"}
	if !reflect.DeepEqual(res.Skipped, want) {
		t.Fatalf("Skipped = %v, want %v", res.Skipped, want)
	}

	noAgents := All
	noAgents.Agents = false
	res, err = Restore(b, noAgents)
	if err != nil {
		t.Fatalf("Restore(no agents): %v", err)
	}
	if res.Agents != 0 || len(res.Skipped) != 0 {
		t.Fatalf("--no-agents touched agents: %+v", res)
	}
}
