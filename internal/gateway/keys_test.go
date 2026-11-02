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

const (
	kPersonal = "sk-personal-0123456789"
	kTeam     = "sk-team-abcdefghijklmn"
	kFree     = "sk-free-qrstuvwxyz0987"
)

// keyed answers per API key: a status and body for each, 200 otherwise, and
// remembers which keys it saw, in order.
type keyed struct {
	mu    sync.Mutex
	fail  map[string]int
	reply string
	seen  []string
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	code := k.fail[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		w.Write([]byte(`{"error":{"message":"rate limited for ` + key + `"}}`))
		return
	}
	w.Write([]byte(k.reply))
}

func (k *keyed) keys() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.seen...)
}

func (k *keyed) reset() {
	k.mu.Lock()
	k.seen = nil
	k.mu.Unlock()
}

// multiKey sets up "relay" with the keys 个人 (front) and 团队, falling back
// to "spare".
func multiKey(t *testing.T, relay, spare http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := httptest.NewServer(relay)
	t.Cleanup(up.Close)
	sp := httptest.NewServer(spare)
	t.Cleanup(sp.Close)
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "spare-key-123456", Models: []string{"m2"}, Chat: sp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: kPersonal, KeyName: "个人", Models: []string{"m1"},
		Fallback: []string{"spare/m2"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "团队", kTeam); err != nil {
		t.Fatal(err)
	}
}

const relayReq = `{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`

func noKeys(t *testing.T, s string) {
	t.Helper()
	for _, k := range []string{kPersonal, kTeam, kFree} {
		if strings.Contains(s, k) {
			t.Fatalf("a full key leaked: %s", s)
		}
	}
}

func TestNextKeyWhenRateLimited(t *testing.T) {
	relay := &keyed{fail: map[string]int{kPersonal: 429}, reply: `{"id":"from-relay","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	multiKey(t, relay, spare)
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(relayReq)))
		return rec.Code, rec.Body.String()
	}
	code, body := send()
	if code != 200 || !strings.Contains(body, "from-relay") || strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if got := relay.keys(); len(got) != 2 || got[0] != kPersonal || got[1] != kTeam {
		t.Fatalf("relay saw %d keys", len(got))
	}
	if spare.keys() != nil {
		t.Fatal("spare tried while a key was left")
	}
	c := s.Recent()[0]
	if c.Provider != "relay" || c.Status != 200 || !strings.Contains(c.Fallback, "relay(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
	noKeys(t, c.Fallback+c.Error+c.ResponseBody)

	// within the minute the failed key rests: the team key goes first
	relay.reset()
	code, body = send()
	if code != 200 || !strings.Contains(body, "from-relay") {
		t.Fatalf("%d %s", code, body)
	}
	if got := relay.keys(); len(got) != 1 || got[0] != kTeam {
		t.Fatalf("relay saw %d keys, first personal: %v", len(got), len(got) > 0 && got[0] == kPersonal)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("recorded %+v", c)
	}
}

func TestUnnamedKeyShowsMasked(t *testing.T) {
	relay := &keyed{fail: map[string]int{kPersonal: 429}, reply: `{"id":"from-relay","choices":[]}`}
	multiKey(t, relay, &keyed{})
	if err := provider.RenameKey("relay", provider.KeyID(kPersonal), ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(relayReq)))
	c := s.Recent()[0]
	if rec.Code != 200 || !strings.Contains(c.Fallback, "relay("+provider.Mask(kPersonal)+"): ") {
		t.Fatalf("%d recorded %+v", rec.Code, c)
	}
	noKeys(t, c.Fallback)
}

func TestKeyOffNeverSent(t *testing.T) {
	relay := &keyed{fail: map[string]int{kPersonal: 429, kTeam: 429}, reply: `{"id":"from-relay","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	multiKey(t, relay, spare)
	if err := provider.AddKey("relay", "free", kFree); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", provider.KeyID(kFree), false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		code, body := post(t, "/v1/chat/completions", relayReq)
		if code != 200 || !strings.Contains(body, "from-spare") {
			t.Fatalf("%d %s", code, body)
		}
	}
	// switching the front off leaves the team key, which is alone on now
	if err := provider.SetKeyOn("relay", provider.KeyID(kPersonal), false); err != nil {
		t.Fatal(err)
	}
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	relay.fail = nil
	code, body := post(t, "/v1/chat/completions", relayReq)
	if code != 200 || !strings.Contains(body, "from-relay") {
		t.Fatalf("%d %s", code, body)
	}
	saw := relay.keys()
	for _, k := range saw {
		if k == kFree {
			t.Fatal("a key that is off was sent upstream")
		}
	}
	if saw[len(saw)-1] != kTeam {
		t.Fatal("the team key did not answer")
	}
}

func TestFallbackAfterAllKeys(t *testing.T) {
	relay := &keyed{fail: map[string]int{kPersonal: 429, kTeam: 402}}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	multiKey(t, relay, spare)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(relayReq)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := relay.keys(); len(got) != 2 || got[0] != kPersonal || got[1] != kTeam {
		t.Fatalf("relay saw %d keys", len(got))
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "relay(个人): ") || !strings.Contains(c.Fallback, "relay(团队): ") {
		t.Fatalf("recorded %+v", c)
	}
	// the spare has one key: its record entry would read "spare: …"
	if strings.Contains(c.Fallback, "spare(") {
		t.Fatalf("recorded %+v", c)
	}
	noKeys(t, c.Fallback)
}

func TestAllKeysAndFallbacksFail(t *testing.T) {
	relay := &keyed{fail: map[string]int{kPersonal: 429, kTeam: 429}}
	spare := &keyed{fail: map[string]int{"spare-key-123456": 503}}
	multiKey(t, relay, spare)
	code, body := post(t, "/v1/chat/completions", relayReq)
	if code != 503 {
		t.Fatalf("%d %s", code, body)
	}
}
