package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// twoProviders: "plan" (the one agents pick) falls back to "spare".
func twoProviders(t *testing.T, plan, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2", "nowhere/x"}}, plan},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, spare},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

const chatReq = `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackWhenOutOfQuota(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"rate limit", 429, "slow down"},
		{"no balance", 402, "Insufficient Balance"},
		{"overloaded", 529, "overloaded"},
		{"quota as 403", 403, "您的套餐额度已用完"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &fake{t: t, ctype: "application/json", code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
			spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
			twoProviders(t, plan, spare)
			code, body := post(t, "/v1/chat/completions", chatReq)
			if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, tc.msg) {
				t.Fatalf("%d %s", code, body)
			}
			if !bytes.Contains(spare.got, []byte(`"model":"m2"`)) {
				t.Fatalf("spare got %s", spare.got)
			}
		})
	}
}

func TestFallbackRestsTheFailedProvider(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Code, rec.Body.String()
	}
	send()
	if c := s.Recent()[0]; c.Provider != "spare" || !strings.Contains(c.Fallback, "plan: ") {
		t.Fatalf("recorded %+v", c)
	}
	plan.got = nil
	if code, body := send(); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.got != nil {
		t.Fatal("a resting provider was tried first")
	}
}

func TestNoFallbackForOtherErrors(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 400 || !strings.Contains(body, "field required") || spare.got != nil {
		t.Fatalf("%d %s (spare got %s)", code, body, spare.got)
	}
}

func TestLastFallbackErrorReachesTheAgent(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"plan limit"}}`}
	spare := &fake{t: t, ctype: "application/json", code: 503, reply: `{"error":{"message":"spare down"}}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 503 || !strings.Contains(body, "spare down") {
		t.Fatalf("%d %s", code, body)
	}
}

// byKey answers per API key: a key named in limited is out of quota.
type byKey struct {
	limited map[string]bool
	seen    []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.seen = append(b.seen, key)
	w.Header().Set("Content-Type", "application/json")
	if b.limited[key] {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func TestSeveralKeysOnTakeOverFromEachOther(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{"k-personal": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-personal", KeyName: "Personal",
		Keys: []provider.KeyAccount{{Name: "Idle", Key: "k-idle", Off: true}, {Name: "Team", Key: "k-team"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Body.String()
	}
	if body := send(); !strings.Contains(body, "from-k-team") {
		t.Fatalf("body %s", body)
	}
	if c := s.Recent()[0]; c.Provider != "plan" || !strings.Contains(c.Fallback, "plan (Personal): ") {
		t.Fatalf("recorded %+v", c)
	}
	// the limited key rests; the one that's off is never tried
	up.seen = nil
	if body := send(); !strings.Contains(body, "from-k-team") || strings.Join(up.seen, ",") != "k-team" {
		t.Fatalf("body %s, tried %v", body, up.seen)
	}
}
