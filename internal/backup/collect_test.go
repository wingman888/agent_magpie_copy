package backup

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// sandbox points HOME and every magpie config path at a fresh temp dir, so
// a test never reads or writes the real machine's configuration.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	return home
}

func mustSaveProvider(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatalf("provider.Save(%s): %v", p.ID, err)
	}
}

func bundleProvider(t *testing.T, b Bundle, id string) provider.Provider {
	t.Helper()
	for _, p := range b.Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q not in bundle", id)
	return provider.Provider{}
}

func TestCollectWithoutKeysStripsSecrets(t *testing.T) {
	sandbox(t)
	mustSaveProvider(t, provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1",
		Key: "sk-PRIMARY-1", KeyName: "main",
		Keys:    []provider.KeyAccount{{Name: "spare", Key: "sk-SPARE-2"}},
		Headers: map[string]string{"X-Api-Key": "HDR-SECRET", "X-Team": "team-blue", "Session-Id": "s1"},
	})

	b, err := Collect(false, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || b.App != "test-build" {
		t.Fatalf("Version=%d App=%q, want 1 and test-build", b.Version, b.App)
	}
	p := bundleProvider(t, b, "acme")
	if p.Key != "" || p.KeyName != "" || len(p.Keys) != 0 {
		t.Fatalf("keys leaked into a keyless backup: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Error("credential header X-Api-Key was kept")
	}
	if _, ok := p.Headers["Session-Id"]; ok {
		t.Error("credential-like header Session-Id was kept")
	}
	if p.Headers["X-Team"] != "team-blue" {
		t.Errorf("plain header X-Team = %q, want team-blue", p.Headers["X-Team"])
	}
}

func TestCollectWithKeysKeepsKeys(t *testing.T) {
	sandbox(t)
	mustSaveProvider(t, provider.Provider{
		ID: "acme", Name: "Acme", Chat: "https://api.acme.test/v1",
		Key: "sk-PRIMARY-1", KeyName: "main",
		Keys:    []provider.KeyAccount{{Name: "spare", Key: "sk-SPARE-2"}},
		Headers: map[string]string{"X-Api-Key": "HDR-SECRET"},
	})

	b, err := Collect(true, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	p := bundleProvider(t, b, "acme")
	if p.Key != "sk-PRIMARY-1" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-SPARE-2" {
		t.Fatalf("keys missing from a keyed backup: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "HDR-SECRET" {
		t.Errorf("header dropped from a keyed backup: %v", p.Headers)
	}
}

func TestCollectIconSettingsProfiles(t *testing.T) {
	sandbox(t)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveProvider(t, provider.Provider{ID: "pic", Name: "Pic", Chat: "https://pic.test/v1", Key: "k", Icon: icon})
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("day", profile.Profile{"claude.model": "pic/m"}); err != nil {
		t.Fatal(err)
	}

	b, err := Collect(true, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(icon, "file:")
	if string(b.Icons[name]) != string(png) {
		t.Errorf("icon %s not collected", name)
	}
	if len(b.Settings) == 0 || !strings.Contains(string(b.Settings), `"dark"`) {
		t.Errorf("settings not collected: %s", b.Settings)
	}
	if !strings.Contains(string(b.Profiles), "pic/m") {
		t.Errorf("profiles not collected: %s", b.Profiles)
	}
}

func TestCollectNoSettingsWhenNoFile(t *testing.T) {
	sandbox(t)
	b, err := Collect(true, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Settings) != 0 {
		t.Errorf("settings collected though none saved: %s", b.Settings)
	}
	if len(b.Profiles) != 0 {
		t.Errorf("profiles collected though none saved: %s", b.Profiles)
	}
}
