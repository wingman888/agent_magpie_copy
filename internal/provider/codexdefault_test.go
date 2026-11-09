package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCdfCodexCacheWithoutGPT55StillListsIt: Codex's own cache may leave
// gpt-5.5 out; the account listing must still offer it.
func TestCdfCodexCacheWithoutGPT55StillListsIt(t *testing.T) {
	acctTEnv(t)
	acctTCodexAuth(t, "chatgpt")
	dir := filepath.Join(os.Getenv("HOME"), ".codex")
	cache := `{"models":[{"slug":"gpt-6-cached","display_name":"Cached","visibility":"list","priority":1,"supported_reasoning_levels":[]}]}`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	p := acctTFind(All(), CodexID)
	if p == nil {
		t.Fatal("codex not listed")
	}
	if !acctTHasModel(p.Available(), "gpt-5.5") {
		t.Fatalf("available lacks gpt-5.5: %+v", p.Available())
	}
	if !acctTHasModel(p.Exposed(), "gpt-5.5") {
		t.Fatalf("exposed lacks gpt-5.5: %+v", p.Exposed())
	}
	if !acctTHasModel(p.Available(), "gpt-6-cached") {
		t.Fatalf("cache entry dropped: %+v", p.Available())
	}
	n := 0
	for _, m := range p.Available() {
		if m.ID == "gpt-5.5" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("gpt-5.5 listed %d times", n)
	}
}
