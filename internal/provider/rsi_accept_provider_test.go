package provider_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

var origEnv = os.Environ()

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func magpieBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "magpie-accept-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "magpie")
		cmd := exec.Command("go", "build", "-o", binPath, "github.com/yetone/magpie")
		cmd.Env = origEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func putProviders(t *testing.T, ps ...map[string]any) {
	t.Helper()
	writeJSON(t, provider.Path(), map[string]any{"providers": ps})
}

func fileProvider(t *testing.T, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Providers {
		if p["id"] == id {
			return p
		}
	}
	t.Fatalf("no %q in providers.json: %s", id, b)
	return nil
}

func fp(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

func TestAcceptStoreIcon(t *testing.T) {
	isolate(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	ref, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q", ref)
	}
	if again, _ := provider.StoreIcon(png); again != ref {
		t.Errorf("same picture stored twice: %q, %q", ref, again)
	}
	p := provider.IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(p); err != nil || string(b) != string(png) {
		t.Fatalf("IconFile(%q) = %q: %v", ref, p, err)
	}
	if ref, err := provider.StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg: %q, %v", ref, err)
	}
	if _, err := provider.StoreIcon([]byte("hello")); err == nil {
		t.Error("text taken as a picture")
	}
	if _, err := provider.StoreIcon(nil); err == nil {
		t.Error("empty picture taken")
	}
	if _, err := provider.StoreIcon(append(png, make([]byte, provider.MaxIcon)...)); err == nil {
		t.Error("oversized picture taken")
	}
	for _, bad := range []string{"../providers.json", "..", "x.png", "0123456789abcdef.exe", "../icons/0123456789abcdef.png", ""} {
		if provider.IconFile(bad) != "" {
			t.Errorf("IconFile(%q) resolved", bad)
		}
	}
}

func TestAcceptIconCleanup(t *testing.T) {
	isolate(t)
	used, _ := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-used"))
	stale, _ := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-stale"))
	fresh, _ := provider.StoreIcon([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-fresh"))
	old := time.Now().Add(-3 * time.Hour)
	for _, ref := range []string{used, stale} {
		os.Chtimes(provider.IconFile(strings.TrimPrefix(ref, "file:")), old, old)
	}
	putProviders(t, map[string]any{"id": "mine", "name": "Mine", "key": "k", "chat": "http://127.0.0.1:1/v1", "icon": used})
	p, err := provider.Find("mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(provider.IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(used) {
		t.Error("a picture in use was removed")
	}
	if !exists(fresh) {
		t.Error("a picture just chosen was removed before its provider was saved")
	}
	if exists(stale) {
		t.Error("an unreferenced old picture was kept")
	}
}

func TestAcceptAddKeyAndKeyProtocol(t *testing.T) {
	isolate(t)
	putProviders(t, map[string]any{"id": "relay", "name": "Relay", "key": "sk-one", "chat": "https://relay.test/v1", "anthropic": "https://relay.test"})
	if err := provider.AddKey("relay", "Team", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "", "sk-two", ""); err == nil {
		t.Error("the same key was added twice")
	}
	if err := provider.AddKey("relay", "", "sk-one", ""); err == nil {
		t.Error("the first key was added again")
	}
	if err := provider.AddKey("relay", "", "sk-bad", "carrier-pigeon"); err == nil {
		t.Error("took an unknown protocol")
	}
	if err := provider.AddKey("relay", "Claude", "sk-three", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	p := fileProvider(t, "relay")
	ks, _ := p["keys"].([]any)
	if p["key"] != "sk-one" || len(ks) != 2 {
		t.Fatalf("keys in file: %v", p)
	}
	k0, k1 := ks[0].(map[string]any), ks[1].(map[string]any)
	if k0["key"] != "sk-two" || k0["name"] != "Team" || k0["off"] == true || k1["key"] != "sk-three" || k1["protocol"] != "anthropic" || k1["off"] == true {
		t.Fatalf("added keys: %v", ks)
	}

	if err := provider.SetKeyProtocol("relay", fp("sk-one"), provider.Chat); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("relay", fp("sk-two"), provider.Responses); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("relay", fp("sk-two"), "carrier-pigeon"); err == nil {
		t.Error("took an unknown protocol")
	}
	if err := provider.SetKeyProtocol("relay", fp("sk-nobody"), provider.Chat); err == nil {
		t.Error("set the protocol of an unknown key")
	}
	p = fileProvider(t, "relay")
	ks, _ = p["keys"].([]any)
	if p["keyProtocol"] != "chat" || ks[0].(map[string]any)["protocol"] != "responses" || ks[1].(map[string]any)["protocol"] != "anthropic" {
		t.Fatalf("protocols: %v", p)
	}
	if err := provider.SetKeyProtocol("relay", fp("sk-two"), ""); err != nil {
		t.Fatal(err)
	}
	if ks, _ = fileProvider(t, "relay")["keys"].([]any); ks[0].(map[string]any)["protocol"] != nil && ks[0].(map[string]any)["protocol"] != "" {
		t.Fatalf("protocol not cleared: %v", ks[0])
	}

	// a provider with no key takes it as its first
	putProviders(t, map[string]any{"id": "local", "name": "Local", "chat": "http://127.0.0.1:1/v1"})
	if err := provider.AddKey("local", "Mine", "sk-first", provider.Chat); err != nil {
		t.Fatal(err)
	}
	p = fileProvider(t, "local")
	if p["key"] != "sk-first" || p["keyName"] != "Mine" || p["keyProtocol"] != "chat" || anyKeys(p) {
		t.Fatalf("first key: %v", p)
	}
}

// See CONTRACT_ADDITIONS.md for the key-management entry points.
func TestAcceptKeyManagement(t *testing.T) {
	isolate(t)
	putProviders(t, map[string]any{"id": "relay", "name": "Relay", "key": "sk-one", "chat": "https://relay.test/v1", "anthropic": "https://relay.test",
		"keyProtocol": "chat", "keys": []any{map[string]any{"name": "Team", "key": "sk-two", "protocol": "anthropic"}}})
	if err := provider.SetKeyOn("relay", fp("sk-two"), true); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("relay", fp("sk-one"), "Personal"); err != nil {
		t.Fatal(err)
	}
	if p := fileProvider(t, "relay"); p["keyName"] != "Personal" {
		t.Fatalf("rename: %v", p)
	}
	// make the second key the one in use: nothing is lost, protocols stay
	if err := provider.UseKey("relay", fp("sk-two")); err != nil {
		t.Fatal(err)
	}
	p := fileProvider(t, "relay")
	ks, _ := p["keys"].([]any)
	if p["key"] != "sk-two" || p["keyProtocol"] != "anthropic" || p["keyName"] != "Team" || len(ks) != 1 {
		t.Fatalf("after use: %v", p)
	}
	if k := ks[0].(map[string]any); k["key"] != "sk-one" || k["protocol"] != "chat" || k["name"] != "Personal" || k["off"] == true {
		t.Fatalf("the replaced key: %v", k)
	}
	// switching off the one in use hands over to the next that is on
	if err := provider.SetKeyOn("relay", fp("sk-two"), false); err != nil {
		t.Fatal(err)
	}
	p = fileProvider(t, "relay")
	ks, _ = p["keys"].([]any)
	if p["key"] != "sk-one" || len(ks) != 1 || ks[0].(map[string]any)["key"] != "sk-two" || ks[0].(map[string]any)["off"] != true {
		t.Fatalf("after off: %v", p)
	}
	// the last one on can be neither turned off nor removed
	if err := provider.SetKeyOn("relay", fp("sk-one"), false); err == nil {
		t.Error("turned off the last key in use")
	}
	if err := provider.RemoveKey("relay", fp("sk-one")); err == nil {
		t.Error("removed the last key in use")
	}
	if p := fileProvider(t, "relay"); p["key"] != "sk-one" {
		t.Fatalf("a refused change was applied: %v", p)
	}
	if err := provider.SetKeyOn("relay", fp("sk-two"), true); err != nil {
		t.Fatal(err)
	}
	if err := provider.RemoveKey("relay", fp("sk-two")); err != nil {
		t.Fatal(err)
	}
	if p := fileProvider(t, "relay"); p["key"] != "sk-one" || anyKeys(p) {
		t.Fatalf("after remove: %v", p)
	}
}

func TestAcceptConfigIsCleanedOnSave(t *testing.T) {
	home := isolate(t)
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com"}),
			"access_token":  jwt(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	putProviders(t,
		map[string]any{"id": "mine", "name": "Mine", "key": "k", "chat": "http://127.0.0.1:1/v1",
			"headers":  map[string]any{" X-Org-Id ": "  acme ", "": "dropped", "  ": "dropped too", "x-Case": "Kept"},
			"fallback": []any{" a/b ", "", "a/b", "c/d"}},
		map[string]any{"id": "bare", "name": "Bare", "key": "k", "chat": "http://127.0.0.1:1/v1", "headers": map[string]any{"": "x"}},
		map[string]any{"id": "deepseek", "name": "DeepSeek", "preset": "deepseek", "key": "k", "chat": "http://127.0.0.1:1/v1",
			"headers": map[string]any{"X-Org-Id": "acme"}},
		map[string]any{"id": "codex", "fallback": []any{"mine/m1", "mine/m1", " "}},
	)
	for _, id := range []string{"mine", "bare", "deepseek", "codex"} {
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
	}
	m := fileProvider(t, "mine")
	hs, _ := m["headers"].(map[string]any)
	if len(hs) != 2 || hs["X-Org-Id"] != "acme" || hs["x-Case"] != "Kept" {
		t.Fatalf("headers: %v", hs)
	}
	if fb, _ := m["fallback"].([]any); len(fb) != 2 || fb[0] != "a/b" || fb[1] != "c/d" {
		t.Fatalf("fallback: %v", m["fallback"])
	}
	if _, has := fileProvider(t, "bare")["headers"]; has {
		t.Error("an empty header map was written")
	}
	if _, has := fileProvider(t, "deepseek")["headers"]; has {
		t.Error("a preset provider kept headers")
	}
	if fb, _ := fileProvider(t, "codex")["fallback"].([]any); len(fb) != 1 || fb[0] != "mine/m1" {
		t.Fatalf("subscription fallback: %v", fb)
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func TestAcceptSeveralCatalogs(t *testing.T) {
	isolate(t)
	cache := os.Getenv("XDG_CACHE_HOME")
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]},
	    "shared": {"id":"shared","name":"Shared from OpenAI"}}},
	  "deepseek": {"models": {
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek V4","reasoning_options":[{"type":"effort","values":["high","max"]}]},
	    "shared": {"id":"shared","name":"Shared from DeepSeek"}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	putProviders(t, map[string]any{"id": "gw", "name": "Gateway", "key": "k", "chat": "https://gw.example/v1", "catalog": "openai,deepseek"})
	p, err := provider.Find("gw")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, m := range p.Available() {
		names[m.ID] = m.Name
	}
	if names["gpt-5.5"] != "GPT-5.5" || names["deepseek-chat"] != "DeepSeek V4" {
		t.Fatalf("available %v", names)
	}
	if names["shared"] != "Shared from OpenAI" {
		t.Errorf("catalogs not searched in the listed order: %q", names["shared"])
	}
	// the vendor's own list, ids prefixed the gateway's way
	live := catalog.Decorate([]catalog.Model{{ID: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "internal-x"}}, p.Available())
	if live[0].Name != "DeepSeek V4" || len(live[0].Efforts) != 2 {
		t.Fatalf("deepseek/deepseek-chat decorated %+v", live[0])
	}
	if live[1].Name != "GPT-5.5" || len(live[1].Efforts) != 2 {
		t.Fatalf("gpt-5.5 decorated %+v", live[1])
	}
	if live[2].Efforts != nil {
		t.Fatalf("unknown model decorated %+v", live[2])
	}
}

// remember runs the real CLI in a fresh process, which is what makes magpie
// note the accounts the agents are signed in to.
func remember(t *testing.T, home string) {
	t.Helper()
	cmd := exec.Command(magpieBin(t), "accounts")
	cmd.Env = append(append([]string{}, origEnv...), "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("magpie accounts: %v\n%s", err, out)
	}
}

func codexLogin(t *testing.T, home, email, refresh string) {
	t.Helper()
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  jwt(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": refresh, "account_id": "acct-" + email,
		},
	})
}

func TestAcceptSetLoginOnAndLoginUsage(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home, "me@example.com", "r-me")
	remember(t, home)
	codexLogin(t, home, "work@example.com", "r-work")
	remember(t, home)

	if err := provider.SetLoginOn("codex", "work@example.com", false); err == nil {
		t.Error("turned off the account the agent is signed in to")
	}
	if err := provider.SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Error("turned on an unknown account")
	}
	if err := provider.SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	used := map[string][2]float64{"acct-me@example.com": {12, 3}, "acct-work@example.com": {97, 40}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		hits.Add(1)
		u := used[r.Header.Get("chatgpt-account-id")]
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": u[0], "limit_window_seconds": 18000},
			"secondary_window": map[string]any{"used_percent": u[1], "limit_window_seconds": 604800}}})
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	u := provider.LoginUsage(context.Background(), "codex")
	me, work := u["me@example.com"], u["work@example.com"]
	if len(u) != 2 || len(me.Windows) != 2 || len(work.Windows) != 2 {
		t.Fatalf("usage %+v", u)
	}
	if me.Windows[0].Used != 12 || me.Windows[1].Used != 3 || work.Windows[0].Used != 97 || work.Windows[1].Used != 40 {
		t.Fatalf("usage %+v", u)
	}
	n := hits.Load()
	if again := provider.LoginUsage(context.Background(), "codex"); len(again) != 2 || again["work@example.com"].Windows[0].Used != 97 {
		t.Fatalf("second call %+v", again)
	}
	if hits.Load() != n {
		t.Errorf("a result less than a minute old was fetched again (%d -> %d requests)", n, hits.Load())
	}
	if got := provider.LoginUsage(context.Background(), "copilot"); len(got) != 0 {
		t.Errorf("usage for an agent without accounts: %v", got)
	}
}

func anyKeys(p map[string]any) bool {
	ks, _ := p["keys"].([]any)
	return len(ks) != 0
}
