package gateway

import (
	"io"
	"net/http"
	"slices"
	"bytes"
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

// keyed serves every key its own status: by bearer token.
type keyed struct {
	codes map[string]int
	seen  []string
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.seen = append(k.seen, tok)
	w.Header().Set("Content-Type", "application/json")
	if c := k.codes[tok]; c != 0 {
		w.WriteHeader(c)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"ok-`+tok+`","choices":[]}`)
}

func keyedSetup(t *testing.T, plan *keyed, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := httptest.NewServer(plan)
	t.Cleanup(up.Close)
	sp := httptest.NewServer(spare)
	t.Cleanup(sp.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "sk-personal-1111", Chat: up.URL + "/v1",
		Models: []string{"m1"}, Fallback: []string{"spare/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k", Chat: sp.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][2]string{{"个人", ""}, {"团队", "sk-team-2222"}, {"", "sk-free-3333"}} {
		if a[1] == "" {
			continue
		}
		if err := provider.AddKey("plan", a[0], a[1]); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := provider.Find("plan")
	provider.RenameKey("plan", p.KeyList()[0].ID, "个人")
}

func TestSecondKeyTakesOverAndRests(t *testing.T) {
	plan := &keyed{codes: map[string]int{"sk-personal-1111": 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Code, rec.Body.String()
	}
	if code, body := send(); code != 200 || !strings.Contains(body, "ok-sk-team-2222") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.HasPrefix(c.Fallback, "plan(个人): ") || strings.Contains(c.Fallback, "sk-personal-1111") || c.Provider != "plan" {
		t.Fatalf("recorded %+v", c)
	}
	plan.seen = nil
	if code, body := send(); code != 200 || !strings.Contains(body, "ok-sk-team-2222") {
		t.Fatalf("%d %s", code, body)
	}
	if len(plan.seen) != 1 || plan.seen[0] != "sk-team-2222" {
		t.Fatalf("resting key tried first: %v", plan.seen)
	}
	if spare.got != nil {
		t.Fatal("fallback used while a key was left")
	}
}

func TestDisabledKeyNeverSent(t *testing.T) {
	plan := &keyed{codes: map[string]int{"sk-personal-1111": 429, "sk-team-2222": 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)
	p, _ := provider.Find("plan")
	var free string
	for _, k := range p.KeyList() {
		if k.Masked == provider.Mask("sk-free-3333") {
			free = k.ID
		}
	}
	if err := provider.SetKeyOn("plan", free, false); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if slices.Contains(plan.seen, "sk-free-3333") || len(plan.seen) != 2 {
		t.Fatalf("seen %v", plan.seen)
	}
}

func TestUnnamedKeyShowsMask(t *testing.T) {
	plan := &keyed{codes: map[string]int{"sk-personal-1111": 429, "sk-team-2222": 429, "sk-free-3333": 429}}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "plan("+provider.Mask("sk-free-3333")+"): ") || strings.Contains(c.Fallback, "sk-free-3333") {
		t.Fatalf("%q", c.Fallback)
	}
	if c.Provider != "spare" || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%+v %s", c, rec.Body.String())
	}
}
