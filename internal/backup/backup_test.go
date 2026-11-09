package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// bkcoreSetup points every config path at a fresh directory and empties PATH,
// so no agent is detected by binary.
func bkcoreSetup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("PATH", t.TempDir())
}

func bkcoreSampleBundle() Bundle {
	return Bundle{
		Version: 1,
		Providers: []provider.Provider{{
			ID:      "relay",
			Name:    "Relay Co",
			Key:     "sk-core-secret-111",
			Chat:    "https://relay.example.test/v1",
			Headers: map[string]string{"X-Api-Key": "hdr-secret-222", "X-Team": "blue"},
		}},
	}
}

func TestBackupCore_SealOpenRoundTrip(t *testing.T) {
	b := bkcoreSampleBundle()
	data, err := Seal(b, "pw-round")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(data, "pw-round")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 1 || len(got.Providers) != 1 {
		t.Fatalf("bundle lost in round trip: %+v", got)
	}
	p := got.Providers[0]
	if p.Key != "sk-core-secret-111" || p.Headers["X-Api-Key"] != "hdr-secret-222" || p.Chat != "https://relay.example.test/v1" {
		t.Fatalf("provider changed in round trip: %+v", p)
	}
}

func TestBackupCore_NoPlaintextInFile(t *testing.T) {
	b := bkcoreSampleBundle()
	b.Profiles = map[string]profile.Profile{"bkcore-profile-name": {"claude.model": "m1"}}
	data, err := Seal(b, "pw-plain")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for _, s := range []string{"sk-core-secret-111", "hdr-secret-222", "relay.example.test", "bkcore-profile-name", "Relay Co"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("file contains plaintext %q", s)
		}
	}
}

func TestBackupCore_WrongPassphrase(t *testing.T) {
	data, err := Seal(bkcoreSampleBundle(), "right-pass")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, err = Open(data, "wrong-pass")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: got %v, want ErrPassphrase", err)
	}
}

func TestBackupCore_SealEmptyPassphrase(t *testing.T) {
	if _, err := Seal(bkcoreSampleBundle(), ""); err == nil {
		t.Fatal("Seal with empty passphrase should fail")
	}
}

func TestBackupCore_DefaultIterationsAndTamper(t *testing.T) {
	data, err := Seal(bkcoreSampleBundle(), "pw-iter")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Fatalf("default iterations not 600000 in header:\n%s", data)
	}
	tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	if _, err := Open(tampered, "pw-iter"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("iterations lowered to 100000: got %v, want ErrPassphrase", err)
	}
	if _, err := Open(data, "pw-iter"); err != nil {
		t.Fatalf("untouched file should open: %v", err)
	}
}

func TestBackupCore_TamperOtherHeaderField(t *testing.T) {
	data, err := Seal(bkcoreSampleBundle(), "pw-hdr")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	m["kdf"] = "pbkdf2-sha256"
	m["iterations"] = float64(600001)
	changed, _ := json.Marshal(m)
	if _, err := Open(changed, "pw-hdr"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("header changed: got %v, want ErrPassphrase", err)
	}
}

func TestBackupCore_NotABackupFormats(t *testing.T) {
	for _, in := range []string{"", "hello", "{}"} {
		_, err := Open([]byte(in), "any")
		if err == nil {
			t.Fatalf("Open(%q) succeeded", in)
		}
		if errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q) reported a passphrase error: %v", in, err)
		}
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("Open(%q) error text mentions passphrase: %v", in, err)
		}
		if !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("Open(%q) = %v, want the not-a-backup message", in, err)
		}
	}
}

func TestBackupCore_NewerVersionNeedsUpgrade(t *testing.T) {
	data, err := Seal(bkcoreSampleBundle(), "pw-ver")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	m["version"] = float64(2)
	m["kdf"] = "something-else" // must not be checked before the version
	newer, _ := json.Marshal(m)
	_, err = Open(newer, "pw-ver")
	if err == nil {
		t.Fatal("version 2 should not open")
	}
	if errors.Is(err, ErrPassphrase) {
		t.Fatalf("version 2 reported as passphrase error: %v", err)
	}
	if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
		t.Errorf("upgrade error text mentions passphrase: %v", err)
	}
	if !strings.Contains(err.Error(), "升级") {
		t.Errorf("version 2 error = %v, want an upgrade message", err)
	}
}

func TestBackupCore_CollectNoKeys(t *testing.T) {
	bkcoreSetup(t)
	if err := provider.Save(provider.Provider{
		ID:      "relay",
		Name:    "Relay",
		Chat:    "https://relay.example.test/v1",
		Key:     "sk-first",
		Headers: map[string]string{"X-Api-Key": "h1", "X-Team": "blue", "Authorization": "x"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := provider.AddKey("relay", "Team", "sk-second", ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	withKeys, err := Collect(true, "test")
	if err != nil {
		t.Fatalf("Collect(true): %v", err)
	}
	if withKeys.Version != 1 || len(withKeys.Providers) != 1 {
		t.Fatalf("Collect(true) = %+v", withKeys)
	}
	if withKeys.Providers[0].Key != "sk-first" || len(withKeys.Providers[0].Keys) != 1 {
		t.Fatalf("Collect(true) dropped keys: %+v", withKeys.Providers[0])
	}

	noKeys, err := Collect(false, "test")
	if err != nil {
		t.Fatalf("Collect(false): %v", err)
	}
	p := noKeys.Providers[0]
	if p.Key != "" || len(p.Keys) != 0 || p.KeyName != "" {
		t.Fatalf("Collect(false) kept a key: %+v", p)
	}
	if _, ok := p.Headers["X-Api-Key"]; ok {
		t.Error("credential header X-Api-Key kept")
	}
	if _, ok := p.Headers["Authorization"]; ok {
		t.Error("credential header Authorization kept")
	}
	if p.Headers["X-Team"] != "blue" {
		t.Errorf("plain header X-Team lost: %v", p.Headers)
	}
}

func TestBackupCore_RestoreKeepsLocalKey(t *testing.T) {
	bkcoreSetup(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://old.example.test/v1", Key: "sk-local"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	incoming := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "relay", Name: "Relay Renamed", Chat: "https://new.example.test/v1",
	}}}
	res, err := Restore(incoming, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Replaced != 1 || res.Added != 0 {
		t.Errorf("counts: added %d replaced %d", res.Added, res.Replaced)
	}
	if len(res.NeedKey) != 0 {
		t.Errorf("provider keeps a local key, but NeedKey = %v", res.NeedKey)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if p.Key != "sk-local" {
		t.Errorf("local key lost: %q", p.Key)
	}
	if p.Name != "Relay Renamed" || p.Chat != "https://new.example.test/v1" {
		t.Errorf("provider not updated: %+v", p)
	}
}

func TestBackupCore_RestoreNeedKeyByName(t *testing.T) {
	bkcoreSetup(t)
	incoming := Bundle{Version: 1, Providers: []provider.Provider{{
		ID: "fresh", Name: "Fresh One", Chat: "https://fresh.example.test/v1",
	}}}
	res, err := Restore(incoming, Parts{Providers: true})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 || res.Replaced != 0 {
		t.Errorf("counts: added %d replaced %d", res.Added, res.Replaced)
	}
	if !reflect.DeepEqual(res.NeedKey, []string{"Fresh One"}) {
		t.Errorf("NeedKey = %v, want the provider name", res.NeedKey)
	}
}

func TestBackupCore_RestoreSkipsReservedAndInvalidIDs(t *testing.T) {
	bkcoreSetup(t)
	incoming := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Chat: "https://x.example.test/v1", Key: "k1"},
		{ID: "Bad ID!", Name: "Bad", Chat: "https://y.example.test/v1", Key: "k2"},
	}}
	res, err := Restore(incoming, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 {
		t.Errorf("invalid ids counted: %+v", res)
	}
}

func TestBackupCore_RestoreUnselectedPartsUntouched(t *testing.T) {
	bkcoreSetup(t)
	incoming := Bundle{
		Version:   1,
		Providers: []provider.Provider{{ID: "onlyprov", Name: "Only", Chat: "https://o.example.test/v1", Key: "k"}},
		Settings:  &settings.Settings{Theme: "dark"},
		Profiles:  map[string]profile.Profile{"bkcore-p": {"claude.model": "m"}},
		Agents:    profile.Profile{"claude.model": "m"},
	}
	res, err := Restore(incoming, Parts{Providers: true})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Added != 1 {
		t.Errorf("provider not added: %+v", res)
	}
	if res.Settings || res.Profiles != 0 || res.Agents != 0 {
		t.Errorf("unselected parts reported: %+v", res)
	}
	if _, err := os.Stat(settings.Path()); !os.IsNotExist(err) {
		t.Error("settings file written although Settings was not selected")
	}
	if ps, _ := profile.Load(); len(ps) != 0 {
		t.Errorf("profiles written although Profiles was not selected: %v", ps)
	}
}

func TestBackupCore_RestoreSettingsAndProfiles(t *testing.T) {
	bkcoreSetup(t)
	if _, err := profile.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := profile.Save("bkcore-existing", profile.Profile{"claude.model": "keep"}); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	incoming := Bundle{
		Version:  1,
		Settings: &settings.Settings{Theme: "dark", Lang: "en"},
		Profiles: map[string]profile.Profile{"bkcore-new": {"claude.model": "new"}},
	}
	res, err := Restore(incoming, All)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !res.Settings || res.Profiles != 1 {
		t.Errorf("result = %+v", res)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "en" {
		t.Errorf("settings not restored: %+v", s)
	}
	ps, err := profile.Load()
	if err != nil {
		t.Fatalf("profile.Load: %v", err)
	}
	if ps["bkcore-existing"]["claude.model"] != "keep" || ps["bkcore-new"]["claude.model"] != "new" {
		t.Errorf("profiles = %v", ps)
	}
}

func TestBackupCore_RestoreAgentsSkipsUninstalled(t *testing.T) {
	bkcoreSetup(t)
	incoming := Bundle{Version: 1, Agents: profile.Profile{"claude.model": "some-model"}}
	res, err := Restore(incoming, Parts{Agents: true})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Agents != 0 {
		t.Errorf("agent changed although not installed: %+v", res)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "本机未安装") {
		t.Errorf("Skipped = %v", res.Skipped)
	}
}

func TestBackupCore_RestoreVersionOneProvidersOnly(t *testing.T) {
	bkcoreSetup(t)
	res, err := Restore(Bundle{Version: 1}, All)
	if err != nil {
		t.Fatalf("empty bundle: %v", err)
	}
	if res.Added != 0 || res.Replaced != 0 || len(res.NeedKey) != 0 || res.Settings ||
		res.Profiles != 0 || res.Agents != 0 || len(res.Skipped) != 0 {
		t.Errorf("empty bundle changed something: %+v", res)
	}
}

func TestBackupCore_CollectRestoreIcon(t *testing.T) {
	bkcoreSetup(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("bkcore-pixels"), 8)...)
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatalf("StoreIcon: %v", err)
	}
	if err := provider.Save(provider.Provider{ID: "iconned", Name: "Iconned", Chat: "https://i.example.test/v1", Key: "k", Icon: icon}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := Collect(true, "test")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	name := strings.TrimPrefix(icon, "file:")
	if !bytes.Equal(b.Icons[name], png) {
		t.Fatalf("icon picture not collected")
	}

	// a fresh machine gets the picture
	if err := os.RemoveAll(provider.IconDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(provider.IconFile(name))
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("icon file not restored: %v", err)
	}

	// an existing picture is never overwritten
	other := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("bkcore-other"), 8)...)
	b.Icons[name] = other
	if _, err := Restore(b, All); err != nil {
		t.Fatalf("second Restore: %v", err)
	}
	got, _ = os.ReadFile(provider.IconFile(name))
	if !bytes.Equal(got, png) {
		t.Error("existing icon was overwritten")
	}
}

func TestBackupCore_BundleSurvivesSealWithKeysOff(t *testing.T) {
	bkcoreSetup(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.test/v1", Key: "sk-gone", Headers: map[string]string{"X-Api-Key": "hv"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	data, err := Seal(b, "pw-off")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(data, []byte("sk-gone")) {
		t.Fatal("key leaked into --no-keys file")
	}
	got, err := Open(data, "pw-off")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Providers[0].Key != "" {
		t.Errorf("key present after --no-keys round trip: %q", got.Providers[0].Key)
	}
}
