package gateway

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolatedHome keeps provider lookups off the machine's own config and logins.
func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
}

// A zero Server has no recent calls, and recording into it must not panic.
func TestRecentOnZeroServer(t *testing.T) {
	var s Server
	if got := s.Recent(); len(got) != 0 {
		t.Fatalf("Recent on zero server = %d calls, want 0", len(got))
	}
	s.record(Call{})
	if got := s.Recent(); len(got) != 1 {
		t.Fatalf("Recent after one record = %d calls, want 1", len(got))
	}
}

// The cooldown tables are written and read from concurrent requests; a zero
// Server must create its table on first write.
func TestCooldownTablesAreSafeConcurrently(t *testing.T) {
	isolatedHome(t)
	var z Server
	z.markCooling("p", "r")
	if !z.cooling("p", "r") {
		t.Fatal("zero server did not record a key cooldown")
	}

	srv := New()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.markCooling("p", "r")
			srv.cooling("p", "r")
			srv.markProviderCooling("p")
			srv.providerCooling("p")
			srv.markCooling("p", "")
		}()
	}
	wg.Wait()
	if !srv.cooling("p", "r") || !srv.providerCooling("p") {
		t.Fatal("cooldowns were not recorded")
	}
	if srv.cooling("p", "") || srv.providerCooling("other") {
		t.Fatal("cooldown leaked to a key or provider it does not name")
	}
}

// Fallback entries that cannot be used are skipped; the provider itself is
// always the first candidate, and an empty provider yields just itself.
func TestCandidatesSkipUnusableFallbackEntries(t *testing.T) {
	isolatedHome(t)
	srv := New()
	p := provider.Provider{ID: "solo", Name: "Solo", Chat: "http://127.0.0.1:9/v1", Key: "sk-solo-key-1",
		Fallback: []string{"", "/", "m", "solo/", "/m", "nope/m", "  ", "solo/m"}}
	if cs := srv.candidates(p, "m"); len(cs) != 1 || cs[0].p.ID != "solo" {
		t.Fatalf("candidates = %d, want only the provider itself", len(cs))
	}
	if cs := srv.candidates(provider.Provider{}, ""); len(cs) != 1 {
		t.Fatalf("candidates for a zero provider = %d, want 1", len(cs))
	}
}

// heldWriter must cope with being used before any header or body is written,
// and with a zero value that has no header map yet.
func TestHeldWriterWithoutHeaderOrBody(t *testing.T) {
	rec := httptest.NewRecorder()
	hw := newHeldWriter(rec)
	hw.Flush()
	hw.release()
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing was written, yet the client got %d %q", rec.Code, rec.Body.String())
	}

	zero := &heldWriter{dst: httptest.NewRecorder()}
	zero.Header().Set("X-A", "b")
	zero.Flush()
	zero.release()

	ok := httptest.NewRecorder()
	w := &heldWriter{dst: ok}
	w.Header().Set("X-Kept", "yes")
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if ok.Body.String() != "ok" || ok.Header().Get("X-Kept") != "yes" {
		t.Fatalf("passed-through reply = %d %q (X-Kept=%q)", ok.Code, ok.Body.String(), ok.Header().Get("X-Kept"))
	}

	held := httptest.NewRecorder()
	h := newHeldWriter(held)
	h.WriteHeader(400)
	h.Write([]byte("bad"))
	h.Flush()
	if held.Body.Len() != 0 {
		t.Fatal("a withheld error reached the client before release")
	}
	h.release()
	if held.Code != 400 || held.Body.String() != "bad" {
		t.Fatalf("released error = %d %q, want 400 \"bad\"", held.Code, held.Body.String())
	}
}
