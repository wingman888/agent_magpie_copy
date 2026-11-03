package gateway

import (
	"bytes"
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

// keyed is an upstream that answers by the key a request was sent with.
// codes maps a key to the status it gets (200 when absent); hits counts the
// requests each key received. A refusal's message echoes the key, the way a
// careless vendor might, so the call record must scrub it.
type keyed struct {
	mu    sync.Mutex
	codes map[string]int
	hits  map[string]int
}

func newKeyed(codes map[string]int) *keyed {
	return &keyed{codes: codes, hits: map[string]int{}}
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.hits[key]++
	code := k.codes[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code == 0 {
		code = http.StatusOK
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"slow down, key `+key+`"}}`)
		return
	}
	io.WriteString(w, `{"id":"answer-ok","choices":[]}`)
}

func (k *keyed) count(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.hits[key]
}

const (
	secretFirst = "sk-personal-key-0001"
	secretTeam  = "sk-team-key-0002"
	secretSpare = "sk-spare-key-0009"
)

// acct is one key of a provider: its name (may be empty) and the key.
type acct struct{ name, key string }

// newEnv points magpie's config at a fresh directory and clears the rest state.
func newEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// serveAt runs an upstream and returns its base URL.
func serveAt(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// planWith saves provider "plan" at url, with the first key as its front key
// and the others added in order through AddKey.
func planWith(t *testing.T, url string, fallback []string, keys ...acct) {
	t.Helper()
	first := keys[0]
	p := provider.Provider{ID: "plan", Name: "Plan", Key: first.key, KeyName: first.name,
		Models: []string{"m1"}, Fallback: fallback, Chat: url + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, a := range keys[1:] {
		if err := provider.AddKey("plan", a.name, a.key); err != nil {
			t.Fatal(err)
		}
	}
}

// saveSpare saves the provider "spare" (model m2) at url.
func saveSpare(t *testing.T, url string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: secretSpare,
		Models: []string{"m2"}, Chat: url + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// keyRef is the list id of the key named name, as the key commands take it.
func keyRef(t *testing.T, id, name string) string {
	t.Helper()
	p, err := provider.Find(id)
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

// sendChat posts through one gateway, so its call records accumulate.
func sendChat(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestSecondKeyAnswersWhenFirstIsRateLimited(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429})
	planWith(t, serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	s := New()
	code, body := sendChat(s, chatReq)
	if code != 200 || !strings.Contains(body, "answer-ok") || strings.Contains(body, "slow down") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count(secretFirst) != 1 || up.count(secretTeam) != 1 {
		t.Fatalf("hits personal=%d team=%d", up.count(secretFirst), up.count(secretTeam))
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || c.Status != 200 {
		t.Fatalf("recorded %+v", c)
	}
	if !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "slow down") {
		t.Fatalf("fallback record %q", c.Fallback)
	}
	if strings.Contains(c.Fallback, secretFirst) {
		t.Fatalf("full key in record: %q", c.Fallback)
	}
}

func TestRestingKeyIsSkippedWithinTheMinute(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429})
	planWith(t, serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	s := New()
	sendChat(s, chatReq)
	if code, body := sendChat(s, chatReq); code != 200 || !strings.Contains(body, "answer-ok") {
		t.Fatalf("second request: %d %s", code, body)
	}
	if up.count(secretFirst) != 1 {
		t.Fatalf("resting key was tried again: %d times", up.count(secretFirst))
	}
	if up.count(secretTeam) != 2 {
		t.Fatalf("team hits %d, want 2", up.count(secretTeam))
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("second request recorded a failed try: %q", c.Fallback)
	}
}

func TestDisabledKeyIsNeverSent(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429})
	planWith(t, serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	if err := provider.SetKeyOn("plan", keyRef(t, "plan", "团队"), false); err != nil {
		t.Fatal(err)
	}
	code, body := sendChat(New(), chatReq)
	if code != 429 || !strings.Contains(body, "slow down") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count(secretTeam) != 0 {
		t.Fatalf("disabled key was sent %d requests", up.count(secretTeam))
	}
}

func TestAllKeysExhaustedUsesFallbackModel(t *testing.T) {
	newEnv(t)
	planUp := newKeyed(map[string]int{secretFirst: 429, secretTeam: 429})
	spareUp := newKeyed(nil)
	planWith(t, serveAt(t, planUp), []string{"spare/m2"}, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	saveSpare(t, serveAt(t, spareUp))
	s := New()
	code, body := sendChat(s, chatReq)
	if code != 200 || !strings.Contains(body, "answer-ok") {
		t.Fatalf("%d %s", code, body)
	}
	if planUp.count(secretFirst) != 1 || planUp.count(secretTeam) != 1 || spareUp.count(secretSpare) != 1 {
		t.Fatalf("hits personal=%d team=%d spare=%d", planUp.count(secretFirst), planUp.count(secretTeam), spareUp.count(secretSpare))
	}
	c := s.Recent()[0]
	if c.Provider != "spare" {
		t.Fatalf("recorded %+v", c)
	}
	if !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "plan(团队): ") {
		t.Fatalf("fallback record %q", c.Fallback)
	}
}

func TestUnnamedKeyIsLabelledByItsMask(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429})
	planWith(t, serveAt(t, up), nil, acct{"", secretFirst}, acct{"团队", secretTeam})
	s := New()
	if code, body := sendChat(s, chatReq); code != 200 || strings.Contains(body, secretFirst) {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if want := "plan(" + provider.Mask(secretFirst) + "): "; !strings.Contains(c.Fallback, want) {
		t.Fatalf("fallback record %q, want it to contain %q", c.Fallback, want)
	}
	if !strings.Contains(c.Fallback, "slow down") {
		t.Fatalf("fallback record lacks the reason: %q", c.Fallback)
	}
	all, _ := json.Marshal(s.Recent())
	for _, k := range []string{secretFirst, secretTeam} {
		if bytes.Contains(all, []byte(k)) {
			t.Fatalf("full key %s in call records: %s", k, all)
		}
	}
}

func TestSingleKeyRecordKeepsItsFormat(t *testing.T) {
	newEnv(t)
	planUp := newKeyed(map[string]int{secretFirst: 429})
	saveSpare(t, serveAt(t, newKeyed(nil)))
	planWith(t, serveAt(t, planUp), []string{"spare/m2"}, acct{"个人", secretFirst})
	s := New()
	if code, body := sendChat(s, chatReq); code != 200 || !strings.Contains(body, "answer-ok") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.HasPrefix(c.Fallback, "plan: ") || strings.Contains(c.Fallback, "plan(") {
		t.Fatalf("single-key fallback record changed: %q", c.Fallback)
	}
}
