package provider

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A brand-new home: nothing signed in, no providers.json, no icons folder.
func TestEmptyHomeAccountsAndUsageDoNotPanicOrWrite(t *testing.T) {
	accountsHome(t)

	if got := Accounts(); len(got) != 0 {
		t.Fatalf("Accounts() on an empty home = %d providers, want 0", len(got))
	}
	if _, err := os.Stat(accountRoot()); !os.IsNotExist(err) {
		t.Fatalf("accounts folder created on an empty home (err=%v)", err)
	}
	list, err := RememberedAccounts("claude")
	if err != nil || len(list) != 0 {
		t.Fatalf("RememberedAccounts = %v, %v; want empty, nil", list, err)
	}
	usage := LoginUsage(context.Background(), "claude")
	if usage == nil || len(usage) != 0 {
		t.Fatalf("LoginUsage = %v; want a non-nil empty map", usage)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if m := LoginUsage(cancelled, "codex"); m == nil || len(m) != 0 {
		t.Fatalf("LoginUsage with a cancelled context = %v; want a non-nil empty map", m)
	}
	if m := LoginUsage(context.Background(), "gemini"); m == nil || len(m) != 0 {
		t.Fatalf("LoginUsage for an unknown agent = %v; want a non-nil empty map", m)
	}
}

// Concurrent Accounts and LoginUsage calls must not race on the caches.
func TestConcurrentAccountsAndUsageAreSafe(t *testing.T) {
	accountsHome(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Accounts()
			if i%2 == 0 {
				resetLoginUsage()
			}
			LoginUsage(context.Background(), "claude")
		}(i)
	}
	wg.Wait()
}

// Saving a provider whose maps and slices are nil, into a config folder that
// does not exist yet, must create the folder and write the provider.
func TestSaveNilMapsIntoMissingFolder(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing", "cfg"))

	p := Provider{ID: "bare", Name: "Bare", Chat: "http://127.0.0.1:9/v1", Key: "sk-bare-key-1"}
	if err := Save(p); err != nil {
		t.Fatalf("Save with nil maps: %v", err)
	}
	got, err := Find("bare")
	if err != nil {
		t.Fatalf("Find after Save: %v", err)
	}
	if got.Headers != nil || got.Fallback != nil || len(got.Keys) != 0 {
		t.Fatalf("nil fields came back non-empty: %+v", got)
	}
	if len(got.ListKeys()) != 1 {
		t.Fatalf("ListKeys = %d, want 1", len(got.ListKeys()))
	}
}

// Credentials, ListKeys and key edits on a provider with no keys at all.
func TestKeyOperationsOnEmptyKeyLists(t *testing.T) {
	isolateConfig(t)

	var none Provider
	if n := len(none.ListKeys()); n != 0 {
		t.Fatalf("ListKeys on zero provider = %d, want 0", n)
	}
	if c := none.Credentials(); len(c) != 1 || c[0].Key != "" {
		t.Fatalf("Credentials on zero provider = %+v, want one empty credential", c)
	}

	if err := Save(Provider{ID: "only", Name: "Only", Chat: "http://127.0.0.1:9/v1", Key: "sk-only-key-1"}); err != nil {
		t.Fatal(err)
	}
	only := KeyRef("sk-only-key-1")
	if err := RemoveKey("only", only); err == nil {
		t.Fatal("removing the last enabled key must fail")
	}
	if err := SetKeyOn("only", only, false); err == nil {
		t.Fatal("disabling the last enabled key must fail")
	}
	if err := RemoveKey("only", "00000000ff"); err == nil {
		t.Fatal("removing an unknown key must fail")
	}
	if err := UseKey("only", "00000000ff"); err == nil {
		t.Fatal("using an unknown key must fail")
	}
}

// A hand-edited list with no key in use: the first enabled entry takes the role.
func TestNormalizePromotesFirstEnabledKeyWhenTopIsEmpty(t *testing.T) {
	p := normalize(Provider{ID: "hand", Keys: []KeyEntry{{Key: "sk-old-one-1", Off: true}, {Key: "sk-new-two-2", Name: "team"}}})
	if p.Key != "sk-new-two-2" || p.KeyName != "team" {
		t.Fatalf("top key = %q (%q), want the enabled one", p.Key, p.KeyName)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-old-one-1" || !p.Keys[0].Off {
		t.Fatalf("other keys = %+v, want the disabled one kept", p.Keys)
	}
	if !p.Ready() {
		t.Fatal("a provider with an enabled key must be ready")
	}

	all := normalize(Provider{ID: "alloff", Keys: []KeyEntry{{Key: "sk-only-off-1", Off: true}}})
	if all.Key != "sk-only-off-1" || all.Keys != nil {
		t.Fatalf("all-off list = top %q, others %+v; want the first key in use", all.Key, all.Keys)
	}
}

// applyHeaders on a request whose header map is nil must not panic.
func TestApplyHeadersToRequestWithNilHeader(t *testing.T) {
	p := Provider{ID: "hdr", Chat: "http://x/v1", Headers: map[string]string{"X-Org-Id": "acme"}}
	req := &http.Request{}
	p.applyHeaders(req)
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("header not set on a nil-header request: %v", req.Header)
	}

	none := Provider{ID: "hdr2", Chat: "http://x/v1"}
	bare := &http.Request{}
	none.applyHeaders(bare)
	if bare.Header != nil {
		t.Fatalf("a provider without headers must not touch the request, got %v", bare.Header)
	}
	none.applyHeaders(nil)
}

// Removing unreferenced icons in a missing folder is a no-op, not a failure.
func TestPruneIconsMissingFolder(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nope"))
	pruneIcons(nil)
	pruneIcons([]Provider{{ID: "a", Icon: "file:0000000000000000.png"}})
}

// Stored icon names: hostile or empty names resolve to nothing and never panic.
func TestIconFileHostileNames(t *testing.T) {
	isolateConfig(t)
	for _, name := range []string{
		"", "..", "../0123456789abcdef.png", "0123456789abcdef.png\x00",
		strings.Repeat("a", 1<<14), "0123456789abcdef.PNG", "/etc/passwd",
	} {
		if got := IconFile(name); got != "" {
			t.Fatalf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

// StoreIcon creates a missing icons folder rather than failing.
func TestStoreIconCreatesMissingFolder(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "fresh"))
	v, err := StoreIcon(testPNG)
	if err != nil {
		t.Fatalf("StoreIcon into a missing folder: %v", err)
	}
	if IconFile(strings.TrimPrefix(v, "file:")) == "" {
		t.Fatalf("stored icon %q does not resolve", v)
	}
}
