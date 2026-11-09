package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	keyfoFirst  = "sk-keyfo-first-aaaa1111"
	keyfoSecond = "sk-keyfo-second-bbbb2222"
	keyfoThird  = "sk-keyfo-third-cccc3333"
)

// keyfoUp is an upstream that answers each key with a set status, and
// records the keys it was asked with, in order.
type keyfoUp struct {
	mu   sync.Mutex
	fail map[string]int
	seen []string
}

func (u *keyfoUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen = append(u.seen, key)
	code := u.fail[key]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-plan","choices":[]}`)
}

func (u *keyfoUp) keys() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func (u *keyfoUp) reset() {
	u.mu.Lock()
	u.seen = nil
	u.mu.Unlock()
}

// keyfoSetup starts a "plan" provider with one key "个人" and fallback
// "spare/m2", and a plain "spare" provider. The keys of plan are added by
// the tests with keyfoAdd.
func keyfoSetup(t *testing.T, plan *keyfoUp, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	planUp := httptest.NewServer(plan)
	t.Cleanup(planUp.Close)
	spareUp := httptest.NewServer(spare)
	t.Cleanup(spareUp.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: keyfoFirst, KeyName: "个人",
		Models: []string{"m1"}, Fallback: []string{"spare/m2"}, Chat: planUp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"},
		Chat: spareUp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func keyfoAdd(t *testing.T, name, key string) {
	t.Helper()
	if err := provider.AddKey("plan", name, key); err != nil {
		t.Fatal(err)
	}
}

func keyfoRef(t *testing.T, name string) string {
	t.Helper()
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		if k.Name == name {
			return k.ID
		}
	}
	t.Fatalf("no key named %q", name)
	return ""
}

// keyfoSend sends the chat request through the server and returns its status and body.
func keyfoSend(s *Server) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

func TestKeyFailoverFirstKeyRateLimited(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)
	keyfoAdd(t, "团队", keyfoSecond)

	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-plan") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := plan.keys(); len(got) != 2 || got[0] != keyfoFirst || got[1] != keyfoSecond {
		t.Fatalf("keys tried: %v", got)
	}
	if spare.got != nil {
		t.Fatal("the fallback was used although a key still worked")
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || !strings.HasPrefix(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
	b, _ := json.Marshal(c)
	for _, k := range []string{keyfoFirst, keyfoSecond} {
		if strings.Contains(string(b), k) {
			t.Fatalf("a full key reached the call record: %s", b)
		}
	}
}

func TestKeyFailoverRestsOnlyTheFailedKey(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)
	keyfoAdd(t, "团队", keyfoSecond)

	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	plan.reset()
	if code, body := keyfoSend(s); code != 200 || !strings.Contains(body, "from-plan") {
		t.Fatalf("%d %s", code, body)
	}
	if got := plan.keys(); len(got) != 1 || got[0] != keyfoSecond {
		t.Fatalf("within the minute the failed key was tried again: %v", got)
	}
}

func TestKeyCooldownExpires(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)
	keyfoAdd(t, "团队", keyfoSecond)

	s := New()
	keyfoSend(s)
	// the first key's rest is over: it goes first again
	rest := "plan#" + keyfoRef(t, "个人")
	restingUntil.Lock()
	restingUntil.m[rest] = time.Now().Add(-time.Second)
	restingUntil.Unlock()
	plan.reset()
	keyfoSend(s)
	if got := plan.keys(); len(got) == 0 || got[0] != keyfoFirst {
		t.Fatalf("an expired key was not tried first: %v", got)
	}
}

func TestDisabledKeyNeverSent(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)
	keyfoAdd(t, "团队", keyfoSecond)
	keyfoAdd(t, "备用", keyfoThird)
	if err := provider.SetKeyOn("plan", keyfoRef(t, "团队"), false); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := keyfoSend(s); code != 200 || !strings.Contains(body, "from-plan") {
		t.Fatalf("%d %s", code, body)
	}
	got := plan.keys()
	if len(got) != 2 || got[0] != keyfoFirst || got[1] != keyfoThird {
		t.Fatalf("keys tried: %v (the disabled key must not be among them)", got)
	}
}

func TestKeysExhaustedThenFallback(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429, keyfoSecond: 429, keyfoThird: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)
	keyfoAdd(t, "团队", keyfoSecond)
	keyfoAdd(t, "", keyfoThird)

	s := New()
	code, body := keyfoSend(s)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if got := plan.keys(); len(got) != 3 {
		t.Fatalf("keys tried before the fallback: %v", got)
	}
	if !strings.Contains(string(spare.got), `"model":"m2"`) {
		t.Fatalf("spare got %s", spare.got)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" {
		t.Fatalf("recorded %+v", c)
	}
	for _, want := range []string{"plan(个人): ", "plan(" + provider.Mask(keyfoThird) + "): "} {
		if !strings.Contains(c.Fallback, want) {
			t.Fatalf("fallback record %q lacks %q", c.Fallback, want)
		}
	}
	if strings.Contains(c.Fallback, keyfoSecond) || strings.Contains(c.Fallback, keyfoThird) {
		t.Fatalf("a full key reached the record: %q", c.Fallback)
	}
}

// A provider with a single enabled key keeps the record format it always had.
func TestSingleKeyRecordFormatUnchanged(t *testing.T) {
	plan := &keyfoUp{fail: map[string]int{keyfoFirst: 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyfoSetup(t, plan, spare)

	s := New()
	if code, body := keyfoSend(s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.HasPrefix(c.Fallback, "plan: ") {
		t.Fatalf("recorded fallback %q", c.Fallback)
	}
}
