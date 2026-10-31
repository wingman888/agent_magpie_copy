package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

type up struct {
	mu    sync.Mutex
	code  int
	ctype string
	reply string
	hits  int
	body  string
	h     http.HandlerFunc
}

func (u *up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.body = string(b)
	u.mu.Unlock()
	if u.h != nil {
		u.h(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.reply)
}
func (u *up) n() int { u.mu.Lock(); defer u.mu.Unlock(); return u.hits }

func env(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func add(t *testing.T, id, model string, fb []string, u *up) {
	t.Helper()
	s := httptest.NewServer(u)
	t.Cleanup(s.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Models: []string{model}, Chat: s.URL + "/v1", Fallback: fb}); err != nil {
		t.Fatal(err)
	}
}

const req = `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`

func send(s *gateway.Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const okBody = `{"id":"from-spare","choices":[]}`

func TestAcceptFallbackOnRetryableErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"429", 429, "slow down"}, {"402", 402, "pay up"}, {"529", 529, "overloaded"},
		{"500", 500, "boom"}, {"408", 408, "timeout"},
		{"403 cn", 403, "您的套餐额度已用完"}, {"400 cn", 400, "余额不足"},
		{"400 en", 400, "You exceeded your current quota"},
		{"401 en", 401, "Insufficient balance"},
		{"403 rate", 403, "Too many requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env(t)
			plan := &up{code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
			spare := &up{reply: okBody}
			add(t, "plan", "m1", []string{"nowhere/x", "spare/m2", "spare/m2"}, plan)
			add(t, "spare", "m2", nil, spare)
			code, body := send(gateway.New(), req)
			if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, tc.msg) {
				t.Fatalf("%d %s", code, body)
			}
			if !strings.Contains(spare.body, `"model":"m2"`) {
				t.Fatalf("spare got %s", spare.body)
			}
		})
	}
}

func TestAcceptFallbackRestAndRecord(t *testing.T) {
	env(t)
	plan := &up{code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &up{reply: okBody}
	add(t, "plan", "m1", []string{"spare/m2"}, plan)
	add(t, "spare", "m2", nil, spare)
	s := gateway.New()
	if code, _ := send(s, req); code != 200 {
		t.Fatal(code)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.HasPrefix(c.Fallback, "plan: ") {
		t.Fatalf("%+v", c)
	}
	before := plan.n()
	code, body := send(s, req)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.n() != before {
		t.Fatal("resting provider was tried")
	}
	c = s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "plan: ") {
		t.Fatalf("second %+v", c)
	}
}

func TestAcceptNoFallbackOnPlain400(t *testing.T) {
	for _, code := range []int{400, 404} {
		env(t)
		plan := &up{code: code, reply: `{"error":{"message":"messages: field required"}}`}
		spare := &up{reply: okBody}
		add(t, "plan", "m1", []string{"spare/m2"}, plan)
		add(t, "spare", "m2", nil, spare)
		s := gateway.New()
		c, body := send(s, req)
		if c != code || !strings.Contains(body, "field required") || spare.n() != 0 {
			t.Fatalf("%d %s spare=%d", c, body, spare.n())
		}
		if s.Recent()[0].Fallback != "" {
			t.Fatal("fallback recorded")
		}
	}
}

func TestAcceptLastErrorWins(t *testing.T) {
	env(t)
	plan := &up{code: 429, reply: `{"error":{"message":"plan limit"}}`}
	spare := &up{code: 503, reply: `{"error":{"message":"spare down"}}`}
	add(t, "plan", "m1", []string{"spare/m2"}, plan)
	add(t, "spare", "m2", nil, spare)
	code, body := send(gateway.New(), req)
	if code != 503 || !strings.Contains(body, "spare down") || strings.Contains(body, "plan limit") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAcceptNoFallbackRecursion(t *testing.T) {
	env(t)
	plan := &up{code: 429, reply: `{"error":{"message":"plan limit"}}`}
	spare := &up{code: 503, reply: `{"error":{"message":"spare down"}}`}
	third := &up{reply: okBody}
	add(t, "plan", "m1", []string{"spare/m2"}, plan)
	add(t, "spare", "m2", []string{"third/m3"}, spare)
	add(t, "third", "m3", nil, third)
	code, _ := send(gateway.New(), req)
	if code != 503 || third.n() != 0 {
		t.Fatalf("%d third=%d", code, third.n())
	}
}

func TestAcceptNoProviderWithoutFallbackUnchanged(t *testing.T) {
	env(t)
	plan := &up{code: 429, reply: `{"error":{"message":"slow"}}`}
	add(t, "plan", "m1", nil, plan)
	s := gateway.New()
	code, body := send(s, req)
	if code != 429 || !strings.Contains(body, "slow") || s.Recent()[0].Fallback != "" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAcceptStreamStartedNeverSwitches(t *testing.T) {
	env(t)
	plan := &up{h: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}}
	spare := &up{reply: okBody}
	add(t, "plan", "m1", []string{"spare/m2"}, plan)
	add(t, "spare", "m2", nil, spare)
	_, body := send(gateway.New(), `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if spare.n() != 0 || strings.Contains(body, "from-spare") {
		t.Fatalf("switched after output began: %s", body)
	}
}
