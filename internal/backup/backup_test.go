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

// isolate points every magpie and agent file at a fresh home, with no
// agent binaries on PATH.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-backup-test")

// fullSetup is a machine with everything a backup carries.
func fullSetup(t *testing.T) {
	t.Helper()
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	err = provider.Save(provider.Provider{
		ID: "relay", Name: "My Relay", Icon: icon,
		Chat: "https://relay.secret-host.example/v1",
		Key:  "sk-primary-SECRET-1111", KeyName: "Personal",
		Keys:    []provider.KeyAccount{{Name: "Team", Key: "sk-team-SECRET-2222"}},
		Headers: map[string]string{"X-Api-Key": "hdr-SECRET-3333", "X-Team": "team-value-4444", "Authorization": "Bearer hdr-SECRET-5555"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Anthropic: "https://other-vendor.example", Key: "sk-other-SECRET-6666"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("secret-profile-name", profile.Profile{"claude.model": "relay/some-model"}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	fullSetup(t)
	b, err := Collect(true, "magpie test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || len(b.Providers) != 2 || len(b.Icons) != 1 || b.Settings == nil || len(b.Profiles) != 1 {
		t.Fatalf("collected %+v", b)
	}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"SECRET", "sk-", "team-value-4444", "secret-host", "other-vendor", "secret-profile-name", "My Relay", "relay"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("%q readable in the file", s)
		}
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) {
		t.Errorf("header: %s", bytes.SplitN(data, []byte("\n"), 2)[0])
	}

	// a fresh machine
	isolate(t)
	got, err := Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Replaced != 0 || len(r.NeedKey) != 0 || !r.Settings || r.Profiles != 1 {
		t.Fatalf("result %+v", r)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-primary-SECRET-1111" || p.KeyName != "Personal" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-team-SECRET-2222" {
		t.Errorf("keys: %+v", p)
	}
	if p.Headers["X-Api-Key"] != "hdr-SECRET-3333" || p.Headers["X-Team"] != "team-value-4444" || len(p.Headers) != 3 {
		t.Errorf("headers: %v", p.Headers)
	}
	name := strings.TrimPrefix(p.Icon, "file:")
	if b, err := os.ReadFile(provider.IconFile(name)); err != nil || !bytes.Equal(b, png) {
		t.Errorf("icon %q: %v", p.Icon, err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Errorf("settings %+v", s)
	}
	ps, _ := profile.Load()
	if ps["secret-profile-name"]["claude.model"] != "relay/some-model" {
		t.Errorf("profiles %v", ps)
	}
	if o, err := provider.Find("other"); err != nil || o.Key != "sk-other-SECRET-6666" {
		t.Errorf("other: %+v %v", o, err)
	}
}

func TestSealOpenErrors(t *testing.T) {
	if _, err := Seal(Bundle{Version: 1}, ""); err == nil {
		t.Error("empty passphrase sealed")
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k"}}}
	data, err := Seal(b, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(data, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, err := Open(data, ""); !errors.Is(err, ErrPassphrase) {
		t.Errorf("empty passphrase: %v", err)
	}
	for _, n := range []string{"100000", "600001", "1", "99999999999"} {
		bad := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": `+n), 1)
		if bytes.Equal(bad, data) {
			t.Fatal("no iterations in the header")
		}
		if _, err := Open(bad, "pw"); !errors.Is(err, ErrPassphrase) {
			t.Errorf("iterations %s: %v", n, err)
		}
	}
	// any other change to the header or the body
	head, body, _ := bytes.Cut(data, []byte("\n"))
	tampered := append(bytes.Replace(head, []byte(`"kdf"`), []byte(` "kdf"`), 1), '\n')
	if _, err := Open(append(tampered, body...), "pw"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("header spacing changed: %v", err)
	}
	flipped := bytes.Clone(data)
	flipped[len(head)+5] ^= 1
	if _, err := Open(flipped, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("body changed: %v", err)
	}
	if got, err := Open(data, "pw"); err != nil || len(got.Providers) != 1 || got.Providers[0].Key != "k" {
		t.Errorf("open: %+v %v", got, err)
	}

	for _, in := range []string{"", "hello", "{}", "null", "[1,2]", `{"format":"other","version":1}`, `{"format":"magpie-backup"}`, "\n\n"} {
		_, err := Open([]byte(in), "pw")
		if !errors.Is(err, ErrNotBackup) || errors.Is(err, ErrPassphrase) {
			t.Errorf("Open(%q) = %v, want not a backup", in, err)
		}
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("Open(%q) talks of the passphrase: %v", in, err)
		}
		if Check([]byte(in)) == nil {
			t.Errorf("Check(%q) passed", in)
		}
	}
	for _, in := range []string{
		`{"format": "magpie-backup", "version": 2}`,
		`{"format": "magpie-backup", "version": 2, "kdf": "argon9", "iterations": 5}` + "\nAAAA\n",
		string(bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 2`), 1)),
	} {
		_, err := Open([]byte(in), "pw")
		if !errors.Is(err, ErrTooNew) || errors.Is(err, ErrPassphrase) || errors.Is(err, ErrNotBackup) {
			t.Errorf("Open(%.40q) = %v, want too new", in, err)
		}
		if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Errorf("too new talks of the passphrase: %v", err)
		}
	}
	if Check(data) != nil {
		t.Error("Check refused a backup")
	}
}

func TestCollectWithoutKeys(t *testing.T) {
	isolate(t)
	fullSetup(t)
	if err := settings.Save(settings.Settings{Proxy: "http://user:pw-SECRET@127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Keys {
		t.Error("marked as with keys")
	}
	for _, p := range b.Providers {
		if p.Key != "" || p.KeyName != "" || len(p.Keys) != 0 {
			t.Errorf("%s keeps a key: %+v", p.ID, p)
		}
		if p.ID == "relay" {
			if _, ok := p.Headers["X-Api-Key"]; ok {
				t.Error("X-Api-Key kept")
			}
			if _, ok := p.Headers["Authorization"]; ok {
				t.Error("Authorization kept")
			}
			if p.Headers["X-Team"] != "team-value-4444" {
				t.Errorf("X-Team lost: %v", p.Headers)
			}
		}
	}
	if b.Settings == nil || strings.Contains(b.Settings.Proxy, "SECRET") {
		t.Errorf("settings %+v", b.Settings)
	}
	data, _ := Seal(b, "pw")
	got, err := Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Providers {
		if p.Key != "" || len(p.Keys) != 0 {
			t.Errorf("opened with a key: %+v", p)
		}
	}
	for _, h := range []string{"Cookie", "x-session-id", "X-Auth-Token", "client_secret", "X-Password"} {
		if !secretHeader(h) {
			t.Errorf("%s not taken for a credential", h)
		}
	}
	if secretHeader("X-Team") || secretHeader("User-Agent") {
		t.Error("an ordinary header taken for a credential")
	}
}

func TestRestoreKeepsLocalKeys(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Local Relay", Chat: "https://old.example/v1", Key: "sk-local",
		Keys: []provider.KeyAccount{{Key: "sk-local-2"}}}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "relay", Name: "Backed Relay", Chat: "https://new.example/v1", Models: []string{"m1"}},
		{ID: "fresh", Name: "Fresh Relay", Chat: "https://fresh.example/v1"},
		{ID: "Bad ID", Name: "Bad", Chat: "https://bad.example/v1", Key: "k"},
		{ID: "magpie", Name: "Gateway", Chat: "https://m.example/v1", Key: "k"},
		{ID: "../x", Name: "Escape", Chat: "https://e.example/v1", Key: "k"},
	}}
	r, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Replaced != 1 {
		t.Errorf("added %d replaced %d", r.Added, r.Replaced)
	}
	if !slices.Equal(r.NeedKey, []string{"Fresh Relay"}) {
		t.Errorf("need key %v", r.NeedKey)
	}
	if r.Settings || r.Profiles != 0 || r.Agents != 0 {
		t.Errorf("result %+v", r)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Backed Relay" || p.Chat != "https://new.example/v1" || !slices.Equal(p.Models, []string{"m1"}) {
		t.Errorf("not updated: %+v", p)
	}
	if p.Key != "sk-local" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-local-2" {
		t.Errorf("local keys lost: %+v", p)
	}
	if f, err := provider.Find("fresh"); err != nil || f.Key != "" {
		t.Errorf("fresh: %+v %v", f, err)
	}
	ids := map[string]bool{}
	for _, p := range provider.Stored() {
		ids[p.ID] = true
	}
	if len(ids) != 2 || ids["magpie"] || ids["bad-id"] {
		t.Errorf("stored %v", ids)
	}

	// a backup with a key replaces the local one
	b = Bundle{Version: 1, Providers: []provider.Provider{{ID: "relay", Name: "R", Chat: "https://new.example/v1", Key: "sk-backup"}}}
	if r, err := Restore(b, All); err != nil || r.Replaced != 1 || r.Added != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if p, _ := provider.Find("relay"); p.Key != "sk-backup" || len(p.Keys) != 0 {
		t.Errorf("backup key not taken: %+v", p)
	}
}

func TestRestorePartsAndEmpty(t *testing.T) {
	home := isolate(t)
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("mine", profile.Profile{"codex.model": "x"}); err != nil {
		t.Fatal(err)
	}
	s := settings.Settings{Theme: "light"}
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "p", Name: "P", Chat: "https://p.example/v1", Key: "k"}},
		Settings:  &s,
		Profiles:  map[string]profile.Profile{"theirs": {"codex.model": "y"}, "mine": {"codex.model": "z"}},
		Agents:    []AgentSetting{{Agent: "cursor", Field: "model", Value: "auto"}},
	}
	r, err := Restore(b, Parts{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Replaced != 0 || r.Settings || r.Profiles != 0 || r.Agents != 0 || len(r.Skipped) != 0 || len(r.NeedKey) != 0 {
		t.Errorf("nothing chosen, yet %+v", r)
	}
	if len(provider.Stored()) != 0 || settings.Load().Theme != "dark" {
		t.Error("an unchosen part was written")
	}

	// no settings in the bundle: the local ones stay
	r, err = Restore(Bundle{Version: 1}, All)
	if err != nil || r.Settings || r.Added != 0 {
		t.Errorf("%+v %v", r, err)
	}
	if settings.Load().Theme != "dark" {
		t.Error("settings overwritten by a bundle without them")
	}

	// profiles merge by name
	r, err = Restore(b, Parts{Profiles: true, Settings: true})
	if err != nil || r.Profiles != 2 || !r.Settings || r.Added != 0 {
		t.Errorf("%+v %v", r, err)
	}
	ps, _ := profile.Load()
	if len(ps) != 2 || ps["mine"]["codex.model"] != "z" || ps["theirs"]["codex.model"] != "y" {
		t.Errorf("profiles %v", ps)
	}
	if settings.Load().Theme != "light" {
		t.Error("settings not restored")
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err == nil {
		t.Error("agents touched")
	}
}

func TestRestoreIcons(t *testing.T) {
	isolate(t)
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(icon, "file:")
	local := []byte("\x89PNG\r\n\x1a\nlocal copy")
	if err := os.WriteFile(provider.IconFile(name), local, 0o644); err != nil {
		t.Fatal(err)
	}
	newName := "0123456789abcdef.png"
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "p", Name: "P", Chat: "https://p.example/v1", Key: "k", Icon: "file:" + newName}},
		Icons: map[string][]byte{
			name:                   png,
			newName:                png,
			"fedcba9876543210.png": make([]byte, provider.MaxIcon+1),
			"../evil.png":          png,
			"0000000000000000.exe": png,
		},
	}
	if _, err := Restore(b, All); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(provider.IconFile(name)); !bytes.Equal(got, local) {
		t.Error("an icon already here was overwritten")
	}
	if got, _ := os.ReadFile(provider.IconFile(newName)); !bytes.Equal(got, png) {
		t.Error("a new icon was not written")
	}
	if _, err := os.Stat(provider.IconFile("fedcba9876543210.png")); err == nil {
		t.Error("an oversized icon was written")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(provider.IconDir()), "evil.png")); err == nil {
		t.Error("an icon escaped its folder")
	}
}

func TestRestoreAgents(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Agents: []AgentSetting{
		{Agent: "cursor", Field: "model", Value: "auto"},
		{Agent: "copilot", Field: "model", Value: "auto"},
		{Agent: "cursor", Field: "nosuchfield", Value: "x"},
	}}
	r, err := Restore(b, Parts{Providers: true, Settings: true, Profiles: true})
	if err != nil || r.Agents != 0 || len(r.Skipped) != 0 {
		t.Fatalf("--no-agents: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "cli-config.json")); err == nil {
		t.Fatal("agent written without Agents")
	}
	r, err = Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if r.Agents != 1 {
		t.Errorf("agents changed %d, skipped %v", r.Agents, r.Skipped)
	}
	if len(r.Skipped) != 2 || !strings.Contains(strings.Join(r.Skipped, "\n"), "Copilot") {
		t.Errorf("skipped %v", r.Skipped)
	}
	got, _ := os.ReadFile(filepath.Join(home, ".cursor", "cli-config.json"))
	if !strings.Contains(string(got), `"auto"`) {
		t.Errorf("cursor config %s", got)
	}
	// the same again changes nothing
	if r, _ := Restore(b, All); r.Agents != 0 {
		t.Errorf("re-applied: %+v", r)
	}
}

func TestCollectSkipsAccountPicks(t *testing.T) {
	isolate(t)
	path := provider.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// model picks kept for a signed-in subscription: no URL, no key
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"claude","name":"","key":"","models":["opus"]},{"id":"x","name":"X","chat":"https://x.example/v1","key":"k"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Collect(true, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 || b.Providers[0].ID != "x" {
		t.Errorf("providers %+v", b.Providers)
	}
}
