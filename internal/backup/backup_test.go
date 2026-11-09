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

// bktSetup points every magpie and agent path at a fresh temp home.
func bktSetup(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

const bktKey = "sk-secret-abc123"
const bktTeamKey = "sk-team-zzz999"
const bktHost = "relay.example.com"

// bktSeed stores one provider with a key, a second key, a credential header,
// a plain header and a picture, plus settings and a profile.
func bktSeed(t *testing.T) {
	t.Helper()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-bkt")
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	err = provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://" + bktHost + "/v1",
		Key: bktKey, Icon: icon,
		Keys:    []provider.KeyAccount{{Name: "team", Key: bktTeamKey}},
		Headers: map[string]string{"X-Api-Key": "hdr-secret-987", "X-Team": "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("bkt-work-profile", profile.Profile{"bktagent.model": "bkt-model-1"}); err != nil {
		t.Fatal(err)
	}
}

func bktFind(t *testing.T, id string) provider.Provider {
	t.Helper()
	for _, p := range provider.All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q not found", id)
	return provider.Provider{}
}

func TestBktRoundTrip(t *testing.T) {
	bktSetup(t)
	bktSeed(t)
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 {
		t.Fatalf("Version = %d, want 1", b.Version)
	}
	data, err := Seal(b, "pw-bkt")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{bktKey, bktTeamKey, "hdr-secret-987", bktHost, "bkt-work-profile", "Relay", "X-Team"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("sealed file contains plaintext %q", secret)
		}
	}

	got, err := Open(data, "pw-bkt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Key != bktKey || len(got.Providers[0].Keys) != 1 {
		t.Fatalf("providers after Open = %+v", got.Providers)
	}
	if got.Providers[0].Headers["X-Api-Key"] != "hdr-secret-987" || got.Providers[0].Headers["X-Team"] != "blue" {
		t.Errorf("headers after Open = %v", got.Providers[0].Headers)
	}
	if len(got.Icons) != 1 {
		t.Errorf("icons after Open = %d, want 1", len(got.Icons))
	}
	if got.Settings == nil || got.Settings.Theme != "dark" {
		t.Errorf("settings after Open = %+v", got.Settings)
	}
	if got.Profiles["bkt-work-profile"]["bktagent.model"] != "bkt-model-1" {
		t.Errorf("profiles after Open = %v", got.Profiles)
	}

	// Restore onto a clean machine.
	bktSetup(t)
	if err := os.RemoveAll(provider.IconDir()); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 || !r.Settings || r.Profiles != 1 {
		t.Errorf("Restore result = %+v", r)
	}
	p := bktFind(t, "relay")
	if p.Key != bktKey || len(p.Keys) != 1 || p.Keys[0].Key != bktTeamKey || p.Headers["X-Api-Key"] != "hdr-secret-987" {
		t.Errorf("restored provider = %+v", p)
	}
	name, _ := strings.CutPrefix(p.Icon, "file:")
	if data, err := os.ReadFile(provider.IconFile(name)); err != nil || !bytes.Equal(data, got.Icons[name]) {
		t.Errorf("restored icon missing or different: %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" {
		t.Errorf("restored settings = %+v", s)
	}
}

func TestBktSealEmptyPassphrase(t *testing.T) {
	bktSetup(t)
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Fatal("Seal with empty passphrase returned no error")
	}
}

func TestBktWrongPassphrase(t *testing.T) {
	bktSetup(t)
	data, err := Seal(Bundle{Version: 1}, "right-bkt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(data, "wrong-bkt"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: err = %v, want ErrPassphrase", err)
	}
}

func TestBktTamperedIterations(t *testing.T) {
	bktSetup(t)
	data, err := Seal(Bundle{Version: 1}, "pw-bkt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("header does not contain the default iterations:\n%s", data)
	}
	for _, n := range []string{"100000", "1000000", "1"} {
		tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": `+n), 1)
		if _, err := Open(tampered, "pw-bkt"); !errors.Is(err, ErrPassphrase) {
			t.Errorf("iterations %s: err = %v, want ErrPassphrase", n, err)
		}
	}
}

func TestBktNotBackup(t *testing.T) {
	bktSetup(t)
	for _, in := range []string{"", "hello", "{}", `{"format":"other","version":1}`} {
		_, err := Open([]byte(in), "pw-bkt")
		if err == nil {
			t.Errorf("Open(%q) returned no error", in)
			continue
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q) = ErrPassphrase, want a not-a-backup error", in)
		}
		if msg := err.Error(); strings.Contains(msg, "passphrase") || strings.Contains(msg, "口令") {
			t.Errorf("Open(%q) message %q mentions the passphrase", in, msg)
		}
	}
}

func TestBktNewerVersion(t *testing.T) {
	bktSetup(t)
	data := []byte(`{"format":"magpie-backup","version":2,"kdf":"from-the-future"}` + "\nAAAA\n")
	_, err := Open(data, "pw-bkt")
	if err == nil {
		t.Fatal("Open of version 2 returned no error")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatal("version 2 reported as a passphrase error")
	}
	if msg := err.Error(); !strings.Contains(msg, "升级") || strings.Contains(msg, "passphrase") || strings.Contains(msg, "口令") {
		t.Errorf("version 2 message = %q", msg)
	}
}

func TestBktNoKeysStripsCredentialHeaders(t *testing.T) {
	bktSetup(t)
	bktSeed(t)
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(b.Providers))
	}
	p := b.Providers[0]
	if p.Key != "" || p.KeyName != "" || p.Keys != nil {
		t.Errorf("keys survived --no-keys: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Errorf("credential header X-Api-Key kept: %v", p.Headers)
	}
	if p.Headers["X-Team"] != "blue" {
		t.Errorf("plain header X-Team dropped: %v", p.Headers)
	}

	data, err := Seal(b, "pw-bkt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(data, "pw-bkt")
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers[0].Key != "" || got.Providers[0].Headers["X-Team"] != "blue" {
		t.Errorf("opened provider = %+v", got.Providers[0])
	}
}

func TestBktRestoreKeepsLocalKey(t *testing.T) {
	bktSetup(t)
	bktSeed(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "Relay", Chat: "https://new.example.com/v1"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Replaced != 1 || r.Added != 0 || len(r.NeedKey) != 0 {
		t.Errorf("Restore result = %+v", r)
	}
	p := bktFind(t, "relay")
	if p.Key != bktKey || len(p.Keys) != 1 || p.Keys[0].Key != bktTeamKey {
		t.Errorf("local keys lost: %+v", p)
	}
	if p.Chat != "https://new.example.com/v1" {
		t.Errorf("provider not updated: chat = %q", p.Chat)
	}
}

func TestBktRestoreNeedKeyByName(t *testing.T) {
	bktSetup(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "bkt-fresh", Name: "Bkt Fresh Co", Chat: "https://fresh.example.com/v1"},
		{ID: "bkt-keyed", Name: "Bkt Keyed", Chat: "https://keyed.example.com/v1", Key: "sk-bkt-keyed"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 {
		t.Errorf("Added = %d, want 2", r.Added)
	}
	if !slices.Equal(r.NeedKey, []string{"Bkt Fresh Co"}) {
		t.Errorf("NeedKey = %v, want [Bkt Fresh Co]", r.NeedKey)
	}
}

func TestBktRestoreIgnoresInvalidAndReservedIDs(t *testing.T) {
	bktSetup(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "Bkt Bad ID!", Name: "Bad", Chat: "https://bad.example.com/v1", Key: "k"},
		{ID: "magpie", Name: "Gateway", Chat: "https://gw.example.com/v1", Key: "k"},
		{ID: "bkt-ok", Name: "Ok", Chat: "https://ok.example.com/v1", Key: "k"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 0 || len(r.NeedKey) != 0 {
		t.Errorf("Restore result = %+v", r)
	}
	for _, p := range provider.All() {
		if p.ID == "magpie" || strings.Contains(p.ID, " ") {
			t.Errorf("invalid provider written: %q", p.ID)
		}
	}
}

func TestBktRestorePartsAndNoSettings(t *testing.T) {
	bktSetup(t)
	if err := settings.Save(settings.Settings{Theme: "light", Lang: "en", Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "bkt-part", Name: "Part", Chat: "https://part.example.com/v1", Key: "k"},
	}}
	r, err := Restore(b, Parts{Settings: true, Profiles: true, Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Settings || r.Profiles != 0 || r.Agents != 0 || len(r.Skipped) != 0 {
		t.Errorf("Restore result = %+v", r)
	}
	if len(provider.All()) != 0 {
		t.Errorf("providers written although Providers part was off")
	}
	if s := settings.Load(); s.Theme != "light" {
		t.Errorf("settings overwritten without a bundle setting: %+v", s)
	}
}

func TestBktRestoreAgentSkipped(t *testing.T) {
	bktSetup(t)
	b := Bundle{Version: 1, Agents: profile.Profile{"bktnoagent.model": "x"}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 0 || len(r.Skipped) != 1 || !strings.Contains(r.Skipped[0], "bktnoagent") {
		t.Errorf("Restore result = %+v", r)
	}
}
