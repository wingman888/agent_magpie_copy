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
	keyPersonal = "sk-personal-aaaa1111"
	keyTeam     = "sk-team-bbbb2222"
	keyFree     = "sk-free-cccc3333"
)

// keyFake is an upstream that answers by the key it is sent: each key has
// its own status and reply. It records the keys it saw, in order.
type keyFake struct {
	mu      sync.Mutex
	replies map[string]keyReply
	seen    []string
}

type keyReply struct {
	code int
	body string
}

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.Header.Get("x-api-key")
	}
	f.mu.Lock()
	f.seen = append(f.seen, tok)
	rep, ok := f.replies[tok]
	f.mu.Unlock()
	if !ok {
		rep = keyReply{code: 401, body: `{"error":{"message":"unknown key"}}`}
	}
	w.Header().Set("Content-Type", "application/json")
	if rep.code != 0 {
		w.WriteHeader(rep.code)
	}
	io.WriteString(w, rep.body)
}

func (f *keyFake) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *keyFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

// resetFailover clears the cooldowns and points magpie's config at a temp dir.
func resetFailover(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// keyedPlan saves provider id with keyPersonal (named 个人) first and
// keyTeam (named 团队) behind it, both enabled, backed by up. Each test uses
// its own id: key cooldowns are kept for the whole process.
func keyedPlan(t *testing.T, id string, up *httptest.Server, fallback ...string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: up.URL + "/v1",
		Key: keyPersonal, KeyName: "个人", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey(id, "团队", keyTeam); err != nil {
		t.Fatal(err)
	}
}

// chatFor is the chat request for model id/m1.
func chatFor(id string) string {
	return `{"model":"` + id + `/m1","messages":[{"role":"user","content":"hi"}]}`
}

func newUpstream(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	return up
}

func sendChat(t *testing.T, s *Server, id string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatFor(id))))
	return rec.Code, rec.Body.String()
}

func TestSecondKeyAnswersWhenFirstIsRateLimited(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {0, `{"id":"from-team","choices":[]}`},
	}}
	keyedPlan(t, "rr-first", newUpstream(t, f))
	s := New()
	code, body := sendChat(t, s, "rr-first")
	if code != 200 || !strings.Contains(body, "from-team") || strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 2 || got[0] != keyPersonal || got[1] != keyTeam {
		t.Fatalf("upstream saw %v", got)
	}
	c := s.Recent()[0]
	if c.Provider != "rr-first" || c.Status != 200 {
		t.Fatalf("recorded %+v", c)
	}
	if !strings.Contains(c.Fallback, "rr-first(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("fallback record %q", c.Fallback)
	}
	for _, leak := range []string{keyPersonal, keyTeam} {
		if strings.Contains(c.Fallback, leak) || strings.Contains(body, leak) {
			t.Fatalf("full key leaked: %q", leak)
		}
	}
}

func TestRestingKeyIsNotTriedFirstWithinCooldown(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {0, `{"id":"from-team","choices":[]}`},
	}}
	keyedPlan(t, "rr-cool", newUpstream(t, f))
	s := New()
	sendChat(t, s, "rr-cool")
	f.reset()
	if code, body := sendChat(t, s, "rr-cool"); code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 1 || got[0] != keyTeam {
		t.Fatalf("resting key was tried first: %v", got)
	}
}

func TestDisabledKeyNeverReachesUpstream(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {0, `{"id":"from-personal","choices":[]}`},
		keyTeam:     {0, `{"id":"from-team","choices":[]}`},
	}}
	keyedPlan(t, "rr-off", newUpstream(t, f))
	if err := provider.SetKeyOn("rr-off", provider.KeyRef(keyTeam), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := sendChat(t, s, "rr-off"); code != 200 || !strings.Contains(body, "from-personal") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 1 || got[0] != keyPersonal {
		t.Fatalf("upstream saw %v", got)
	}
}

func TestDisabledKeyStaysOutOfFailover(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {0, `{"id":"from-team","choices":[]}`},
	}}
	keyedPlan(t, "rr-off-fail", newUpstream(t, f))
	if err := provider.SetKeyOn("rr-off-fail", provider.KeyRef(keyTeam), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendChat(t, s, "rr-off-fail")
	if code != 429 || strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 1 || got[0] != keyPersonal {
		t.Fatalf("disabled key was used: %v", got)
	}
}

func TestAllKeysFailThenFallbackModel(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {429, `{"error":{"message":"plan limit"}}`},
	}}
	keyedPlan(t, "rr-all", newUpstream(t, f), "spare/m2")
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	spareUp := newUpstream(t, spare)
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Chat: spareUp.URL + "/v1",
		Key: keyFree, Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendChat(t, s, "rr-all")
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 2 {
		t.Fatalf("plan keys tried: %v", got)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "rr-all(个人): ") || !strings.Contains(c.Fallback, "rr-all(团队): ") {
		t.Fatalf("recorded %+v", c)
	}
}

// A single enabled key keeps the old record format: "provider: reason".
func TestSingleKeyRecordFormatUnchanged(t *testing.T) {
	resetFailover(t)
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	s := New()
	sendChat(t, s, "plan")
	c := s.Recent()[0]
	if !strings.HasPrefix(c.Fallback, "plan: ") || strings.Contains(c.Fallback, "plan(") {
		t.Fatalf("fallback record %q", c.Fallback)
	}
}

// A key without a name is shown by its masked form, never in full.
func TestUnnamedKeyRecordedMasked(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {0, `{"id":"from-personal","choices":[]}`},
		keyTeam:     {429, `{"error":{"message":"rate limited"}}`},
	}}
	up := newUpstream(t, f)
	if err := provider.Save(provider.Provider{ID: "rr-unnamed", Name: "Plan", Chat: up.URL + "/v1",
		Key: keyTeam, Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("rr-unnamed", "", keyPersonal); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendChat(t, s, "rr-unnamed")
	if code != 200 || !strings.Contains(body, "from-personal") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	want := "rr-unnamed(" + provider.Mask(keyTeam) + "): "
	if !strings.Contains(c.Fallback, want) {
		t.Fatalf("fallback record %q, want it to contain %q", c.Fallback, want)
	}
	if strings.Contains(c.Fallback, keyTeam) || strings.Contains(body, keyTeam) {
		t.Fatalf("full key leaked in record %q", c.Fallback)
	}
}

// Within the cooldown a key is not sent at all: when every key of a provider
// is resting, the provider is skipped and the request goes to its fallback
// without the upstream seeing a single request for the provider.
func TestRestingKeysSkipProviderForFallback(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {429, `{"error":{"message":"plan limit"}}`},
	}}
	keyedPlan(t, "rr-skip", newUpstream(t, f), "spare-skip/m2")
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	spareUp := newUpstream(t, spare)
	if err := provider.Save(provider.Provider{ID: "spare-skip", Name: "Spare", Chat: spareUp.URL + "/v1",
		Key: keyFree, Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	sendChat(t, s, "rr-skip")
	f.reset()
	code, body := sendChat(t, s, "rr-skip")
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 0 {
		t.Fatalf("resting keys were sent to upstream: %v", got)
	}
}

// When every key is resting and there is no fallback, nothing is sent and
// the call fails at once, naming no key.
func TestRestingKeysWithoutFallbackFailWithoutSending(t *testing.T) {
	resetFailover(t)
	f := &keyFake{replies: map[string]keyReply{
		keyPersonal: {429, `{"error":{"message":"rate limited"}}`},
		keyTeam:     {429, `{"error":{"message":"plan limit"}}`},
	}}
	keyedPlan(t, "rr-nofb", newUpstream(t, f))
	s := New()
	sendChat(t, s, "rr-nofb")
	f.reset()
	code, body := sendChat(t, s, "rr-nofb")
	if code == 200 || strings.Contains(body, keyPersonal) || strings.Contains(body, keyTeam) {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.keys(); len(got) != 0 {
		t.Fatalf("resting keys were sent to upstream: %v", got)
	}
	if c := s.Recent()[0]; c.Provider != "rr-nofb" || c.Status == 200 {
		t.Fatalf("recorded %+v", c)
	}
}
