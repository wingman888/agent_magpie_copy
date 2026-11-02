package backup

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// fresh points every magpie and agent file at an empty home.
func fresh(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir()) // no agent binaries
	return home
}

var png = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

// setup is a machine with a bit of everything.
func setup(t *testing.T) (icon string) {
	t.Helper()
	icon, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "My Relay", Icon: icon, Chat: "https://relay.secret-host.example/v1", Key: "sk-relay-primary-1111",
		Keys:    []provider.KeyAccount{{Name: "Team", Key: "sk-relay-team-2222"}},
		Headers: map[string]string{"X-Api-Key": "hdr-credential-value", "X-Team": "team-blue-value", "Authorization": "Bearer hdr-bearer-zzz"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-deep-3333", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("secret-profile-name", profile.Profile{"claude.model": "deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	return icon
}

func TestRoundTrip(t *testing.T) {
	fresh(t)
	icon := setup(t)
	b, err := Collect(true, "magpie test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || b.App != "magpie test" || len(b.Providers) != 2 || b.Settings == nil || len(b.Profiles) != 1 || len(b.Icons) != 1 {
		t.Fatalf("collect: %+v", b)
	}
	data, err := Seal(b, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"iterations": 600000`)) || !bytes.Contains(data, []byte(`"format": "magpie-backup"`)) {
		t.Fatalf("header: %s", data[:bytes.IndexByte(data, '\n')])
	}
	for _, secret := range []string{"sk-relay-primary-1111", "sk-relay-team-2222", "hdr-credential-value", "team-blue-value", "secret-host", "deepseek.com", "secret-profile-name", "My Relay"} {
		if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString([]byte(secret)))) {
			t.Fatalf("%q is readable in the file", secret)
		}
	}

	// a brand new machine
	fresh(t)
	got, err := Open(data, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Replaced != 0 || len(res.NeedKey) != 0 || !res.Settings || res.Profiles != 1 || res.Icons != 1 {
		t.Fatalf("result %+v", res)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-relay-primary-1111" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-relay-team-2222" || p.Headers["X-Api-Key"] != "hdr-credential-value" || p.Headers["X-Team"] != "team-blue-value" || p.Icon != icon {
		t.Fatalf("relay %+v", p)
	}
	name, _ := strings.CutPrefix(icon, "file:")
	if b, err := os.ReadFile(provider.IconFile(name)); err != nil || !bytes.Equal(b, png) {
		t.Fatalf("icon %v", err)
	}
	if s := settings.Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("settings %+v", s)
	}
	ps, _ := profile.Load()
	if ps["secret-profile-name"]["claude.model"] != "deepseek/pro" {
		t.Fatalf("profiles %+v", ps)
	}
	if st, err := os.Stat(provider.Path()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("providers.json mode %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	fresh(t)
	data, err := Seal(Bundle{Version: 1, Providers: []provider.Provider{{ID: "x", Name: "X", Chat: "https://x.test/v1", Key: "k"}}}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(Bundle{}, ""); err == nil {
		t.Fatal("an empty passphrase sealed")
	}
	if _, err := Open(data, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := Open(data, ""); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("empty passphrase: %v", err)
	}
	tampered := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 100000`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("no iterations to change")
	}
	if _, err := Open(tampered, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("fewer iterations: %v", err)
	}
	huge := bytes.Replace(data, []byte(`"iterations": 600000`), []byte(`"iterations": 999999999999`), 1)
	if _, err := Open(huge, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("huge iterations: %v", err)
	}
	// a flipped byte in the ciphertext
	body := slices.Clone(data)
	i := bytes.IndexByte(body, '\n') + 5
	if body[i] == 'A' {
		body[i] = 'B'
	} else {
		body[i] = 'A'
	}
	if _, err := Open(body, "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("changed body: %v", err)
	}
	if _, err := Open(bytes.Replace(data, []byte(`"kdf": "pbkdf2-sha256"`), []byte(`"kdf": "scrypt"`), 1), "pw"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("changed kdf: %v", err)
	}

	for _, in := range []string{"", "hello", "{}", "[1,2]", `{"format": "other", "version": 1}`, "\n\n"} {
		_, err := Open([]byte(in), "pw")
		if !errors.Is(err, ErrNotBackup) || errors.Is(err, ErrPassphrase) || strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Fatalf("%q: %v", in, err)
		}
	}
	newer := bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 2`), 1)
	for _, in := range [][]byte{newer, []byte(`{"format": "magpie-backup", "version": 2}`), []byte(`{"format":"magpie-backup","version":3,"kdf":"what"}`)} {
		_, err := Open(in, "pw")
		if !errors.Is(err, ErrTooNew) || errors.Is(err, ErrPassphrase) || strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "口令") {
			t.Fatalf("%s: %v", in, err)
		}
	}
	if b, err := Open(data, "pw"); err != nil || len(b.Providers) != 1 {
		t.Fatalf("open: %v", err)
	}
}

func TestNoKeys(t *testing.T) {
	fresh(t)
	setup(t)
	b, err := Collect(false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Providers {
		if p.Key != "" || len(p.Keys) != 0 {
			t.Fatalf("%s kept a key: %+v", p.ID, p)
		}
		if p.ID == "relay" {
			if _, ok := p.Headers["X-Api-Key"]; ok {
				t.Fatal("X-Api-Key kept")
			}
			if _, ok := p.Headers["Authorization"]; ok {
				t.Fatal("Authorization kept")
			}
			if p.Headers["X-Team"] != "team-blue-value" {
				t.Fatalf("X-Team lost: %v", p.Headers)
			}
		}
	}
	data, _ := Seal(b, "pw")
	got, _ := Open(data, "pw")
	for _, s := range []string{"sk-relay", "sk-deep", "hdr-credential", "hdr-bearer"} {
		plain := strings.Join(func() []string {
			var out []string
			for _, p := range got.Providers {
				out = append(out, p.Key, p.KeyName)
				for _, k := range p.Keys {
					out = append(out, k.Key)
				}
				for _, v := range p.Headers {
					out = append(out, v)
				}
			}
			return out
		}(), " ")
		if strings.Contains(plain, s) {
			t.Fatalf("%q in a keyless backup", s)
		}
	}

	// another machine that has relay with its own key, and nothing else
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Old", Chat: "https://old.test/v1", Key: "sk-local-9999",
		Keys:    []provider.KeyAccount{{Key: "sk-local-second"}},
		Headers: map[string]string{"X-Api-Key": "local-hdr"}}); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(got, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 1 || !slices.Equal(res.NeedKey, []string{"DeepSeek"}) {
		t.Fatalf("result %+v", res)
	}
	p, _ := provider.Find("relay")
	if p.Name != "My Relay" || p.Chat != "https://relay.secret-host.example/v1" || p.Key != "sk-local-9999" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-local-second" {
		t.Fatalf("relay %+v", p)
	}
	if p.Headers["X-Api-Key"] != "local-hdr" || p.Headers["X-Team"] != "team-blue-value" {
		t.Fatalf("headers %v", p.Headers)
	}
	d, err := provider.Find("deepseek")
	if err != nil || d.Key != "" {
		t.Fatalf("deepseek %+v %v", d, err)
	}
}

func TestRestoreHandmade(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "magpie", Name: "Gateway", Chat: "https://m.test/v1", Key: "k"},
		{ID: "Bad ID", Name: "Bad", Chat: "https://b.test/v1", Key: "k"},
		{ID: "../x", Name: "Bad", Chat: "https://b.test/v1", Key: "k"},
		{ID: "good", Name: "Good", Chat: "https://g.test/v1"},
	}, Icons: map[string][]byte{"../../evil.png": png, "0123456789abcdef.png": bytes.Repeat([]byte{1}, provider.MaxIcon+1)}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Replaced != 0 || !slices.Equal(res.NeedKey, []string{"Good"}) || res.Settings || res.Profiles != 0 || res.Agents != 0 || res.Icons != 0 {
		t.Fatalf("result %+v", res)
	}
	if s := settings.Load(); s.Theme != "light" {
		t.Fatal("settings overwritten without any in the backup")
	}
	ps := provider.Stored()
	if len(ps) != 1 || ps[0].ID != "good" {
		t.Fatalf("stored %+v", ps)
	}
	// once more: replaced
	res, _ = Restore(b, All)
	if res.Added != 0 || res.Replaced != 1 {
		t.Fatalf("again %+v", res)
	}
}

// A provider left with neither a key nor key accounts needs one, even one
// that works without (a local server); one that has an account doesn't.
func TestNeedKeyLiteral(t *testing.T) {
	fresh(t)
	b := Bundle{Version: 1, Providers: []provider.Provider{
		{ID: "local", Name: "Local LLM", Chat: "http://localhost:11434/v1"},
		{ID: "multi", Name: "Multi", Chat: "https://m.test/v1", Keys: []provider.KeyAccount{{Key: "sk-a"}}},
	}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || !slices.Equal(res.NeedKey, []string{"Local LLM"}) {
		t.Fatalf("result %+v", res)
	}
}

func TestIconsNotOverwritten(t *testing.T) {
	fresh(t)
	name := "00112233445566aa.png"
	path := provider.IconFile(name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("mine"), 0o644)
	res, err := Restore(Bundle{Version: 1, Icons: map[string][]byte{name: png, "ffeeddccbbaa9988.png": png}}, All)
	if err != nil || res.Icons != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Fatal("icon overwritten")
	}
}

func TestParts(t *testing.T) {
	fresh(t)
	setup(t)
	b, err := Collect(true, "")
	if err != nil {
		t.Fatal(err)
	}
	b.Agents = map[string]string{"claude.model": "deepseek/pro"}
	home := fresh(t)
	claudePath := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(claudePath), 0o755)
	os.WriteFile(claudePath, []byte(`{"theme":"dark"}`), 0o644)

	res, err := Restore(b, Parts{Profiles: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Settings || res.Profiles != 1 || res.Agents != 0 || len(provider.Stored()) != 0 {
		t.Fatalf("profiles only %+v", res)
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		t.Fatal("settings written")
	}
	if v, _ := edit.GetJSON(claudePath, "model"); v != "" {
		t.Fatal("agent touched")
	}
	res, err = Restore(b, Parts{Providers: true, Settings: true, Profiles: true}) // --no-agents
	if err != nil || res.Added != 2 || !res.Settings || res.Agents != 0 {
		t.Fatalf("no agents %+v %v", res, err)
	}
	if v, _ := edit.GetJSON(claudePath, "model"); v != "" {
		t.Fatal("agent touched")
	}
}

func TestRestoreAgents(t *testing.T) {
	home := fresh(t)
	claudePath := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(claudePath), 0o755)
	os.WriteFile(claudePath, []byte(`{"theme":"dark"}`), 0o644)
	b := Bundle{Version: 1,
		Providers: []provider.Provider{{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}},
		Agents: map[string]string{
			"claude.model": "deepseek/pro",   // resolves through the restored provider
			"claude.haiku": "nosuch/model",   // fails: not a model magpie serves
			"codex.model":  "deepseek/flash", // codex isn't installed
			"claude.bogus": "x",
		}}
	res, err := Restore(b, All)
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 1 {
		t.Fatalf("agents %+v", res)
	}
	if v, _ := edit.GetJSON(claudePath, "model"); v != "deepseek/pro" {
		t.Fatalf("claude model %q", v)
	}
	skipped := strings.Join(res.Skipped, "\n")
	for _, want := range []string{"claude.haiku", "codex.model", "not installed", "claude.bogus"} {
		if !strings.Contains(skipped, want) {
			t.Fatalf("skipped %q lacks %q", skipped, want)
		}
	}
	if strings.Contains(skipped, "claude.model") {
		t.Fatalf("claude.model skipped: %q", skipped)
	}
	// the snapshot on this machine now carries it
	c, err := Collect(true, "")
	if err != nil || c.Agents["claude.model"] != "deepseek/pro" {
		t.Fatalf("collect agents %+v %v", c.Agents, err)
	}
}
