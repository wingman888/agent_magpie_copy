package gateway

import (
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
	personalKey = "sk-personal-0000000001"
	teamKey     = "sk-team-00000000002"
)

// keyedUpstream answers each request by the bearer key it carries: code gives
// the status for a key (absent means 200), and a 200 says tag[key] in its id.
type keyedUpstream struct {
	mu   sync.Mutex
	code map[string]int
	tag  map[string]string
	hits map[string]int
}

func newKeyedUpstream() *keyedUpstream {
	return &keyedUpstream{code: map[string]int{}, tag: map[string]string{}, hits: map[string]int{}}
}

func (u *keyedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.hits[key]++
	code, tag := u.code[key], u.tag[key]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+tag+`","choices":[]}`)
}

func (u *keyedUpstream) hit(key string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[key]
}

// keyedSetup: "plan" holds two keys, personal (front) and team, and falls
// back to "spare/m2", a plain fake upstream. planKeys is the upstream the
// plan's keys talk to.
func keyedSetup(t *testing.T, planKeys http.Handler, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()

	planUp := httptest.NewServer(planKeys)
	t.Cleanup(planUp.Close)
	spareUp := httptest.NewServer(spare)
	t.Cleanup(spareUp.Close)

	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k", Chat: spareUp.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: personalKey, KeyName: "个人",
		Chat: planUp.URL + "/v1", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "团队", teamKey); err != nil {
		t.Fatal(err)
	}
}

// keyRef is the id KeyList gives the key with this secret.
func keyRef(t *testing.T, key string) string {
	t.Helper()
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		if k.Masked == provider.Mask(key) {
			return k.ID
		}
	}
	t.Fatalf("no key %s on plan", provider.Mask(key))
	return ""
}

func sendChat(t *testing.T, s *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

func TestKeyFailsOverWithinProvider(t *testing.T) {
	up := newKeyedUpstream()
	up.code[personalKey], up.tag[teamKey] = 429, "team"
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, up, spare)
	s := New()

	code, body := sendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
	if strings.Contains(c.Fallback, personalKey) || strings.Contains(c.Error, personalKey) {
		t.Fatalf("full key in record: %+v", c)
	}
	if up.hit(personalKey) != 1 || up.hit(teamKey) != 1 || spare.got != nil {
		t.Fatalf("hits personal=%d team=%d spare=%s", up.hit(personalKey), up.hit(teamKey), spare.got)
	}
}

func TestKeyRestsForAMinute(t *testing.T) {
	up := newKeyedUpstream()
	up.code[personalKey], up.tag[teamKey] = 429, "team"
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, up, spare)
	s := New()
	sendChat(t, s)

	if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if up.hit(personalKey) != 1 {
		t.Fatalf("resting key was tried again: %d", up.hit(personalKey))
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "plan" {
		t.Fatalf("recorded %+v", c)
	}
}

func TestDisabledKeyIsNeverSent(t *testing.T) {
	up := newKeyedUpstream()
	up.code[personalKey] = 429
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, up, spare)
	if err := provider.SetKeyOn("plan", keyRef(t, teamKey), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	for i := 0; i < 2; i++ {
		if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
			t.Fatalf("%d %s", code, body)
		}
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
	}
	if up.hit(teamKey) != 0 {
		t.Fatalf("disabled key was sent %d requests", up.hit(teamKey))
	}
}

func TestAllKeysFailUsesFallback(t *testing.T) {
	up := newKeyedUpstream()
	up.code[personalKey], up.code[teamKey] = 429, 429
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, up, spare)
	s := New()

	if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "plan(团队): ") {
		t.Fatalf("recorded %+v", c)
	}
	if strings.Contains(c.Fallback, personalKey) || strings.Contains(c.Fallback, teamKey) {
		t.Fatalf("full key in record: %s", c.Fallback)
	}
	if up.hit(personalKey) != 1 || up.hit(teamKey) != 1 {
		t.Fatalf("hits personal=%d team=%d", up.hit(personalKey), up.hit(teamKey))
	}
	// the provider as a whole now rests, so the next request goes straight to the spare
	if code, _ := sendChat(t, s); code != 200 || up.hit(personalKey) != 1 || up.hit(teamKey) != 1 {
		t.Fatalf("code %d, plan tried again: personal=%d team=%d", code, up.hit(personalKey), up.hit(teamKey))
	}
}

func TestUnnamedKeyIsRecordedByMask(t *testing.T) {
	first, second := "sk-first-0000000001", "sk-second-000000002"
	up := newKeyedUpstream()
	up.code[first] = 429
	up.tag[second] = "second"
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, up, spare)
	// replace the named keys with unnamed ones
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	p.Key, p.KeyName, p.Keys = first, "", nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "", second); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-second") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	want := "plan(" + provider.Mask(first) + "): "
	if !strings.Contains(c.Fallback, want) || strings.Contains(c.Fallback, first) {
		t.Fatalf("recorded %q, want it to say %q", c.Fallback, want)
	}
}
