package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// accSpecHome points config, cache and home at fresh temp dirs.
func accSpecHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// accSpecRef is the key fingerprint the spec defines: hex of sha256(key)[:5].
func accSpecRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// accSpecPNG is a PNG-signed byte string of a given total length.
func accSpecPNG(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), []byte("accspec")...)
	for len(b) < n {
		b = append(b, 0)
	}
	return b
}

var accSpecIconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// accSpecFile reads providers.json and returns the raw providers array.
func accSpecFile(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Providers
}

// accSpecEntry returns the raw object of one provider in providers.json.
func accSpecEntry(t *testing.T, id string) map[string]any {
	t.Helper()
	for _, p := range accSpecFile(t) {
		if p["id"] == id {
			return p
		}
	}
	t.Fatalf("no %q in providers.json", id)
	return nil
}

// a. StoreIcon / IconFile -----------------------------------------------------

func TestAccSpecIconStoreAcceptsPNGAndSVG(t *testing.T) {
	accSpecHome(t)
	png := accSpecPNG(64)
	v, err := StoreIcon(png)
	if err != nil {
		t.Fatalf("PNG: %v", err)
	}
	if !strings.HasPrefix(v, "file:") {
		t.Fatalf("icon value %q lacks file: prefix", v)
	}
	name := strings.TrimPrefix(v, "file:")
	if !accSpecIconName.MatchString(name) {
		t.Fatalf("icon name %q does not match 16 hex + extension", name)
	}
	if !strings.HasSuffix(name, ".png") {
		t.Fatalf("PNG stored as %q", name)
	}
	path := IconFile(name)
	if path == "" {
		t.Fatalf("IconFile(%q) is empty for a stored name", name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stored icon missing at %s: %v", path, err)
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`)
	sv, err := StoreIcon(svg)
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	if !strings.HasPrefix(sv, "file:") || !strings.HasSuffix(sv, ".svg") || !accSpecIconName.MatchString(strings.TrimPrefix(sv, "file:")) {
		t.Fatalf("SVG icon value = %q", sv)
	}
}

func TestAccSpecIconSameContentStoredOnce(t *testing.T) {
	accSpecHome(t)
	png := accSpecPNG(64)
	a, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same image gave %q then %q", a, b)
	}
	dir := filepath.Dir(IconFile(strings.TrimPrefix(a, "file:")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("icons folder has %d files after storing one image twice", n)
	}
}

func TestAccSpecIconRejectsEmptyOversizeAndText(t *testing.T) {
	accSpecHome(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty icon accepted")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Error("zero-length icon accepted")
	}
	if _, err := StoreIcon(accSpecPNG(MaxIcon + 1)); err == nil {
		t.Error("icon of MaxIcon+1 bytes accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text, not an image")); err == nil {
		t.Error("plain text accepted as an icon")
	}
}

func TestAccSpecIconFileRejectsBadNames(t *testing.T) {
	accSpecHome(t)
	for _, bad := range []string{"../x.png", "a/b.png", "zz.png", "0123456789abcdef.txt", ""} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
	v, err := StoreIcon(accSpecPNG(64))
	if err != nil {
		t.Fatal(err)
	}
	if IconFile(strings.TrimPrefix(v, "file:")) == "" {
		t.Error("IconFile rejected a stored name")
	}
}

// b. Keys -----------------------------------------------------------------------

func TestAccSpecKeyLifecycle(t *testing.T) {
	accSpecHome(t)
	const k1, k2, k3 = "sk-accspec-one-111", "sk-accspec-two-222", "sk-accspec-three-333"
	if err := Save(Provider{ID: "gw", Name: "GW", Key: k1, KeyName: "personal", Chat: "https://gw.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("gw", "team", k2, ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := AddKey("gw", "dup", k2, ""); err == nil {
		t.Error("duplicate key accepted")
	}
	if err := AddKey("gw", "dup", k1, ""); err == nil {
		t.Error("duplicate of the current key accepted")
	}
	if err := AddKey("gw", "odd", k3, "openai"); err == nil {
		t.Error("unknown protocol accepted by AddKey")
	}
	p := accSpecFind(t, "gw")
	if p.Key != k1 || p.KeyName != "personal" || len(p.Keys) != 1 || p.Keys[0].Key != k2 || p.Keys[0].Off {
		t.Fatalf("after AddKey: %+v", p)
	}

	// UseKey keeps names and protocols of both
	if err := SetKeyProtocol("gw", accSpecRef(k1), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("gw", accSpecRef(k2)); err != nil {
		t.Fatal(err)
	}
	p = accSpecFind(t, "gw")
	if p.Key != k2 || p.KeyName != "team" {
		t.Fatalf("UseKey: current = %q named %q", p.Key, p.KeyName)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != k1 || p.Keys[0].Name != "personal" || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("UseKey lost the other key's name or protocol: %+v", p.Keys)
	}

	// turning off the current first key hands over, old one kept off
	if err := SetKeyOn("gw", accSpecRef(k2), false); err != nil {
		t.Fatalf("SetKeyOn off current: %v", err)
	}
	p = accSpecFind(t, "gw")
	if p.Key != k1 || p.KeyName != "personal" || p.KeyProtocol != Anthropic {
		t.Fatalf("hand-over: current = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	off := false
	for _, k := range p.Keys {
		if k.Key == k2 {
			off = k.Off
		}
	}
	if !off {
		t.Fatalf("the old first key was not kept as off: %+v", p.Keys)
	}

	// now the only enabled key is k1: it cannot be disabled or removed, nothing changes
	if err := SetKeyOn("gw", accSpecRef(k1), false); err == nil {
		t.Error("last enabled key was disabled")
	}
	if err := RemoveKey("gw", accSpecRef(k1)); err == nil {
		t.Error("last enabled key was removed")
	}
	p = accSpecFind(t, "gw")
	if p.Key != k1 {
		t.Fatalf("a refused change moved the current key to %q", p.Key)
	}

	// rename
	if err := RenameKey("gw", accSpecRef(k2), "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("gw", accSpecRef(k1), "current-renamed"); err != nil {
		t.Fatal(err)
	}
	p = accSpecFind(t, "gw")
	if p.KeyName != "current-renamed" || p.Keys[0].Name != "renamed" {
		t.Fatalf("rename: keyName=%q extra=%q", p.KeyName, p.Keys[0].Name)
	}
}

func TestAccSpecFirstKeyOfKeylessProvider(t *testing.T) {
	accSpecHome(t)
	// a local server needs no key, so the provider is saved without one
	if err := Save(Provider{ID: "loc", Name: "Loc", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("loc", "first", "sk-accspec-loc-1", ""); err != nil {
		t.Fatal(err)
	}
	p := accSpecFind(t, "loc")
	if p.Key != "sk-accspec-loc-1" || len(p.Keys) != 0 {
		t.Fatalf("first key = %q with extras %+v", p.Key, p.Keys)
	}
}

func TestAccSpecKeyRefIsSHA256Prefix(t *testing.T) {
	ref := KeyRef("sk-accspec-x")
	if len(ref) != 10 || ref != accSpecRef("sk-accspec-x") {
		t.Fatalf("KeyRef = %q", ref)
	}
}

// c. SetKeyProtocol ----------------------------------------------------------------

func TestAccSpecSetKeyProtocol(t *testing.T) {
	accSpecHome(t)
	const k1, k2 = "sk-accspec-p-1", "sk-accspec-p-2"
	if err := Save(Provider{ID: "pr", Name: "Pr", Key: k1, Chat: "https://pr.example.com/v1", Responses: "https://pr.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("pr", "two", k2, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("pr", accSpecRef(k2), Responses); err != nil {
		t.Fatal(err)
	}
	if got := accSpecFind(t, "pr").Keys[0].Protocol; got != Responses {
		t.Fatalf("protocol set to %q", got)
	}
	if err := SetKeyProtocol("pr", accSpecRef(k2), ""); err != nil {
		t.Fatal(err)
	}
	if got := accSpecFind(t, "pr").Keys[0].Protocol; got != "" {
		t.Fatalf("protocol not cleared: %q", got)
	}
	for _, bad := range []Protocol{"openai", "gemini"} {
		if err := SetKeyProtocol("pr", accSpecRef(k2), bad); err == nil {
			t.Errorf("SetKeyProtocol accepted %q", bad)
		}
	}
	if err := SetKeyProtocol("pr", "0000000000", Chat); err == nil {
		t.Error("SetKeyProtocol accepted a key that is not there")
	}
}

// d. SetLoginOn ----------------------------------------------------------------------

func TestAccSpecSetLoginOnUnknownAndCurrent(t *testing.T) {
	home := savedHome(t)
	fakeClaudeWho(t, map[string]string{"tok-spec": "spec@example.com"})
	writeClaudeLogin(t, home, "tok-spec")
	if err := SetLoginOn("claude", "spec@example.com", false); err == nil {
		t.Error("the signed-in account was turned off")
	}
	if err := SetLoginOn("claude", "missing@example.com", true); err == nil {
		t.Error("unknown account accepted")
	}
}

// e. LoginUsage ------------------------------------------------------------------------

func TestAccSpecLoginUsageUnsupportedAgentEmpty(t *testing.T) {
	accSpecHome(t)
	got := LoginUsage(context.Background(), "gemini")
	if len(got) != 0 {
		t.Fatalf("unsupported agent gave %d entries", len(got))
	}
}

// f. headers -------------------------------------------------------------------------

func TestAccSpecHeadersSavedAndTrimmed(t *testing.T) {
	accSpecHome(t)
	if err := Save(Provider{ID: "hdr", Name: "Hdr", Key: "k-accspec", Chat: "https://h.example.com/v1",
		Headers: map[string]string{"  X-Org-Id  ": "  acme  ", "   ": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	p := accSpecFind(t, "hdr")
	if !reflect.DeepEqual(p.Headers, map[string]string{"X-Org-Id": "acme"}) {
		t.Fatalf("headers = %#v", p.Headers)
	}
	raw := accSpecEntry(t, "hdr")["headers"]
	m, ok := raw.(map[string]any)
	if !ok || m["X-Org-Id"] != "acme" || len(m) != 1 {
		t.Fatalf("headers in providers.json = %#v", raw)
	}
}

func TestAccSpecEmptyHeadersNotWritten(t *testing.T) {
	accSpecHome(t)
	if err := Save(Provider{ID: "plain", Name: "Plain", Key: "k-accspec", Chat: "https://p.example.com/v1",
		Headers: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if _, has := accSpecEntry(t, "plain")["headers"]; has {
		t.Fatal("empty header map was written to providers.json")
	}
}

func TestAccSpecPresetIgnoresHeaders(t *testing.T) {
	h := map[string]string{"X-Org-Id": "acme"}
	if got := (Provider{Preset: "deepseek", Headers: h}).CustomHeaders(); got != nil {
		t.Fatalf("preset sends %v", got)
	}
	if got := (Provider{ID: "custom", Headers: h}).CustomHeaders(); !reflect.DeepEqual(got, h) {
		t.Fatalf("custom provider sends %v", got)
	}
}

// g. fallback ------------------------------------------------------------------------

func TestAccSpecFallbackCleanedDedupedAndKept(t *testing.T) {
	accSpecHome(t)
	if err := Save(Provider{ID: "fb", Name: "FB", Key: "k-accspec", Chat: "https://fb.example.com/v1",
		Fallback: []string{" a/m1 ", "", "a/m1", "b/m2", "   "}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a/m1", "b/m2"}
	if got := accSpecFind(t, "fb").Fallback; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %q, want %q", got, want)
	}
	if err := SetFallback("fb", []string{"  c/m3 ", "c/m3", ""}); err != nil {
		t.Fatal(err)
	}
	if got := accSpecFind(t, "fb").Fallback; !reflect.DeepEqual(got, []string{"c/m3"}) {
		t.Fatalf("SetFallback = %q", got)
	}
}

func TestAccSpecFallbackKeptOnSignedInAccount(t *testing.T) {
	home := savedHome(t)
	fakeClaudeWho(t, map[string]string{"tok-fb": "fb@example.com"})
	writeClaudeLogin(t, home, "tok-fb")
	accts := Accounts()
	if len(accts) == 0 {
		t.Fatal("no signed-in account to test with")
	}
	id := accts[0].ID
	if err := SetFallback(id, []string{" x/y ", "x/y", ""}); err != nil {
		t.Fatal(err)
	}
	for _, p := range All() {
		if p.ID == id {
			if !reflect.DeepEqual(p.Fallback, []string{"x/y"}) {
				t.Fatalf("account fallback = %q", p.Fallback)
			}
			return
		}
	}
	t.Fatalf("account %s not listed", id)
}

// h. catalog -------------------------------------------------------------------------

func TestAccSpecMultiCatalogNamesAndEfforts(t *testing.T) {
	accSpecHome(t)
	writeCatalogs(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","release_date":"2026-01-01",
	      "reasoning_options":[{"type":"effort","values":["low","high"]}],"modalities":{"output":["text"]}}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat","release_date":"2025-01-01",
	      "reasoning_options":[{"type":"effort","values":["medium"]}],"modalities":{"output":["text"]}}}}
	}`)
	if err := Save(Provider{ID: "gw", Name: "GW", Key: "k-accspec", Chat: "https://gw.example.com/v1",
		Catalog: "openai,deepseek"}); err != nil {
		t.Fatal(err)
	}
	p := accSpecFind(t, "gw")
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog = %q", p.Catalog)
	}
	avail := p.Available()
	ds, ok := findModel(avail, "deepseek/deepseek-chat")
	if !ok || ds.Name != "DeepSeek Chat" || !reflect.DeepEqual(ds.Efforts, []string{"medium"}) {
		t.Fatalf("deepseek/deepseek-chat = %+v, %v", ds, ok)
	}
	gpt, ok := findModel(avail, "gpt-5.5")
	if !ok || gpt.Name != "GPT-5.5" || !reflect.DeepEqual(gpt.Efforts, []string{"low", "high"}) {
		t.Fatalf("gpt-5.5 = %+v, %v", gpt, ok)
	}
	// Decorate, for a live list that carries the prefixed id
	dec := catalog.Decorate([]catalog.Model{{ID: "deepseek/deepseek-chat"}}, avail)
	if len(dec) != 1 || dec[0].Name != "DeepSeek Chat" || !reflect.DeepEqual(dec[0].Efforts, []string{"medium"}) {
		t.Fatalf("Decorate = %+v", dec)
	}
}

// helpers -----------------------------------------------------------------------------

func accSpecFind(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatalf("Find(%q): %v", id, err)
	}
	return *p
}
