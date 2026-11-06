package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyFake is an upstream that answers by the key it was sent: codes maps a
// key to a failing status, and any other key gets a normal answer.
type keyFake struct {
	mu    sync.Mutex
	codes map[string]int
	seen  []string
}

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.seen = append(f.seen, k)
	code := f.codes[k]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+strings.TrimPrefix(k, "key-")+`","choices":[]}`)
}

func (f *keyFake) sawKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

func (f *keyFake) reset() {
	f.mu.Lock()
	f.seen = nil
	f.mu.Unlock()
}

// setupPool makes provider "pool" with two keys, "个人" (key-personal) and
// "团队" (key-team), falling back to "spare" with one key.
func setupPool(t *testing.T, pool, spare *keyFake, teamName string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	restingKeys.Lock()
	restingKeys.m = map[string]time.Time{}
	restingKeys.Unlock()
	up := httptest.NewServer(pool)
	t.Cleanup(up.Close)
	upS := httptest.NewServer(spare)
	t.Cleanup(upS.Close)
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "key-spare",
		Models: []string{"m2"}, Chat: upS.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "pool", Name: "Pool", Key: "key-personal", KeyName: "个人",
		Models: []string{"m1"}, Chat: up.URL + "/v1", Fallback: []string{"spare/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("pool", teamName, "key-team"); err != nil {
		t.Fatal(err)
	}
}

const poolReq = `{"model":"pool/m1","messages":[{"role":"user","content":"hi"}]}`

func sendTo(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestKeysFailOverToNextKey(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "团队")
	s := New()
	code, body := sendTo(s, poolReq)
	if code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"key-personal", "key-team"}; !reflect.DeepEqual(pool.sawKeys(), want) {
		t.Fatalf("pool saw %v, want %v", pool.sawKeys(), want)
	}
	if spare.sawKeys() != nil {
		t.Fatal("the fallback was used although a key answered")
	}
	c := s.Recent()[0]
	if c.Provider != "pool" || !strings.Contains(c.Fallback, "pool(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
	if strings.Contains(c.Fallback, "key-personal") || strings.Contains(c.Error, "key-personal") {
		t.Fatalf("full key recorded: %+v", c)
	}
}

func TestRestingKeyIsSkippedWithinCooldown(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "团队")
	s := New()
	sendTo(s, poolReq)
	pool.reset()
	if code, body := sendTo(s, poolReq); code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"key-team"}; !reflect.DeepEqual(pool.sawKeys(), want) {
		t.Fatalf("pool saw %v, want only %v", pool.sawKeys(), want)
	}
}

func TestDisabledKeyIsNeverSent(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "团队")
	p, err := provider.Find("pool")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("pool", p.KeyList()[1].ID, false); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, poolReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"key-personal"}; !reflect.DeepEqual(pool.sawKeys(), want) {
		t.Fatalf("pool saw %v, want %v", pool.sawKeys(), want)
	}
	if c := s.Recent()[0]; strings.Contains(c.Fallback, "团队") {
		t.Fatalf("the disabled key was named: %+v", c)
	}
}

func TestAllKeysFailThenFallbackModel(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429, "key-team": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "团队")
	s := New()
	code, body := sendTo(s, poolReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"key-personal", "key-team"}; !reflect.DeepEqual(pool.sawKeys(), want) {
		t.Fatalf("pool saw %v, want %v", pool.sawKeys(), want)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "pool(个人): ") || !strings.Contains(c.Fallback, "pool(团队): ") {
		t.Fatalf("recorded %+v", c)
	}
}

func TestAllKeysRestingPutsProviderBehindFallback(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429, "key-team": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "团队")
	s := New()
	sendTo(s, poolReq)
	pool.reset()
	spare.reset()
	if code, body := sendTo(s, poolReq); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if pool.sawKeys() != nil {
		t.Fatalf("resting keys were tried first: %v", pool.sawKeys())
	}
}

func TestUnnamedKeyIsRecordedByMask(t *testing.T) {
	pool := &keyFake{codes: map[string]int{"key-personal": 429, "key-team": 429}}
	spare := &keyFake{}
	setupPool(t, pool, spare, "")
	s := New()
	if code, _ := sendTo(s, poolReq); code != 200 {
		t.Fatalf("%d", code)
	}
	c := s.Recent()[0]
	want := "pool(" + provider.Mask("key-team") + "): "
	if !strings.Contains(c.Fallback, want) {
		t.Fatalf("want %q in %q", want, c.Fallback)
	}
	if strings.Contains(c.Fallback, "key-team") || strings.Contains(c.Fallback, "key-personal") {
		t.Fatalf("full key recorded: %q", c.Fallback)
	}
}
