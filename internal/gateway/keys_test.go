package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// bank is an upstream that answers per bearer token.
type bank struct {
	mu   sync.Mutex
	code map[string]int
	seen []string
}

func (b *bank) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.seen = append(b.seen, tok)
	code := b.code[tok]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		return
	}
	w.Write([]byte(`{"id":"from-` + tok + `","choices":[]}`))
}

func (b *bank) reset() { b.mu.Lock(); b.seen = nil; b.mu.Unlock() }

func multiKey(t *testing.T, up *bank, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	u := httptest.NewServer(up)
	t.Cleanup(u.Close)
	p := provider.Provider{ID: "plan", Name: "Plan", Key: "sk-personal-0000", KeyName: "个人", Chat: u.URL + "/v1", Models: []string{"m1"}}
	if spare != nil {
		s := httptest.NewServer(spare)
		t.Cleanup(s.Close)
		p.Fallback = []string{"spare/m2"}
		if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "sk-spare-0000", Models: []string{"m2"}, Chat: s.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "团队", "sk-team-0000"); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "", "sk-free-00000000"); err != nil {
		t.Fatal(err)
	}
}

func TestKeyFailoverAndRest(t *testing.T) {
	up := &bank{code: map[string]int{"sk-personal-0000": 429}}
	multiKey(t, up, nil)
	p, _ := provider.Find("plan")
	if err := provider.SetKeyOn("plan", p.KeyList()[2].ID, false); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Code, rec.Body.String()
	}
	if code, body := send(); code != 200 || !strings.Contains(body, "from-sk-team-0000") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "plan(个人): ") || strings.Contains(c.Fallback, "sk-personal") {
		t.Fatalf("recorded %q", c.Fallback)
	}
	up.reset()
	if code, _ := send(); code != 200 {
		t.Fatal(code)
	}
	if len(up.seen) != 1 || up.seen[0] != "sk-team-0000" {
		t.Fatalf("rest ignored: %v", up.seen)
	}
	for _, k := range up.seen {
		if k == "sk-free-00000000" {
			t.Fatal("disabled key used")
		}
	}
}

func TestKeysThenFallbackProvider(t *testing.T) {
	up := &bank{code: map[string]int{"sk-personal-0000": 429, "sk-team-0000": 429, "sk-free-00000000": 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiKey(t, up, spare)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	f := s.Recent()[0].Fallback
	if !strings.Contains(f, "plan(个人): ") || !strings.Contains(f, "plan(团队): ") || !strings.Contains(f, "plan(sk-f…0000)") && !strings.Contains(f, "plan(") || strings.Contains(f, "sk-team-0000") {
		t.Fatalf("recorded %q", f)
	}
	if len(up.seen) != 3 {
		t.Fatalf("seen %v", up.seen)
	}
}
