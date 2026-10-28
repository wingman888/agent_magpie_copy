package gateway_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

type upstream struct {
	mu    sync.Mutex
	got   [][]byte
	code  int
	ctype string
	reply string
	hdr   map[string]string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got = append(u.got, b)
	u.mu.Unlock()
	w.Header().Set("Content-Type", u.ctype)
	for k, v := range u.hdr {
		w.Header().Set(k, v)
	}
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.reply)
}

func (u *upstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.got)
}

func (u *upstream) last() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.got) == 0 {
		return nil
	}
	return u.got[len(u.got)-1]
}

func jsonUp(code int, reply string) *upstream {
	return &upstream{code: code, ctype: "application/json", reply: reply}
}

// chain registers main (falling back to the given entries) and spare under
// ids unique to the test, since resting state is shared by the process.
func chain(t *testing.T, id string, main, spare *upstream, fb ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := httptest.NewServer(main)
	t.Cleanup(m.Close)
	s := httptest.NewServer(spare)
	t.Cleanup(s.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: "Main", Key: "k", Models: []string{"m1"}, Chat: m.URL + "/v1", Fallback: fb}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: id + "sp", Name: "Spare", Key: "k", Models: []string{"m2"}, Chat: s.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func send(s *gateway.Server, model string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

func TestAcceptFallbackOnQuotaErrors(t *testing.T) {
	for i, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"429", 429, "slow down"},
		{"402", 402, "Insufficient Balance"},
		{"529", 529, "overloaded"},
		{"500", 500, "boom internal"},
		{"408", 408, "timed out waiting"},
		{"403 cn quota", 403, "您的套餐额度已用完"},
		{"401 rate limit", 401, "Rate-Limit hit"},
		{"400 too many", 400, "Too Many Requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "accq" + string(rune('a'+i))
			main := jsonUp(tc.code, `{"error":{"message":"`+tc.msg+`"}}`)
			spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
			chain(t, id, main, spare, id+"sp/m2")
			rec := send(gateway.New(), id+"/m1")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") || strings.Contains(rec.Body.String(), tc.msg) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if !bytes.Contains(spare.last(), []byte(`"model":"m2"`)) {
				t.Fatalf("spare got %s", spare.last())
			}
		})
	}
}

func TestAcceptPlainErrorsAreNotFallenBack(t *testing.T) {
	for i, tc := range []struct {
		code int
		body string
	}{
		{400, `{"error":{"message":"messages: field required"}}`},
		{401, `{"error":{"message":"bad api key"}}`},
		{403, `{"error":{"message":"forbidden"}}`},
		{404, `{"error":{"message":"no such model"}}`},
		{422, `{"error":{"message":"quota"}}`},
	} {
		id := "accp" + string(rune('a'+i))
		main := jsonUp(tc.code, tc.body)
		spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
		chain(t, id, main, spare, id+"sp/m2")
		rec := send(gateway.New(), id+"/m1")
		if rec.Code != tc.code || !rec.Body.String() == tc.body {
			t.Fatalf("%d: got %d %s", tc.code, rec.Code, rec.Body.String())
		}
		if spare.calls() != 0 {
			t.Fatalf("%d: spare was asked", tc.code)
		}
	}
}

func TestAcceptNoFallbackConfigured(t *testing.T) {
	main := jsonUp(429, `{"error":{"message":"rate limited here"}}`)
	spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
	chain(t, "accn", main, spare)
	rec := send(gateway.New(), "accn/m1")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "rate limited here") || spare.calls() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if c := gateway.New(); len(c.Recent()) != 0 {
		t.Fatal("fresh server has calls")
	}
}

func TestAcceptLastCandidateErrorIsPassedThrough(t *testing.T) {
	main := jsonUp(429, `{"error":{"message":"plan limit"}}`)
	spare := jsonUp(503, `{"error":{"message":"spare down"}}`)
	chain(t, "accl", main, spare, "acclsp/m2")
	rec := send(gateway.New(), "accl/m1")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "spare down") || strings.Contains(rec.Body.String(), "plan limit") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("content type: %v", rec.Header())
	}
}

func TestAcceptFailedProviderRests(t *testing.T) {
	main := jsonUp(429, `{"error":{"message":"rate limited"}}`)
	spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
	chain(t, "accr", main, spare, "accrsp/m2", "nowhere/x")
	s := gateway.New()
	if rec := send(s, "accr/m1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rc := s.Recent()
	if len(rc) != 1 || rc[0].Provider != "accrsp" || !strings.HasPrefix(rc[0].Fallback, "accr: ") {
		t.Fatalf("recorded %+v", rc)
	}
	if main.calls() != 1 {
		t.Fatalf("main calls %d", main.calls())
	}
	rec := send(s, "accr/m1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if main.calls() != 1 {
		t.Fatal("a resting provider was tried first")
	}
	if rc := s.Recent(); len(rc) != 2 || rc[0].Provider != "accrsp" {
		t.Fatalf("recorded %+v", rc)
	}
}

func TestAcceptSuccessRecordsNoFallback(t *testing.T) {
	main := jsonUp(0, `{"id":"from-main","choices":[]}`)
	spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
	chain(t, "accs", main, spare, "accssp/m2")
	s := gateway.New()
	if rec := send(s, "accs/m1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-main") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rc := s.Recent()
	if len(rc) != 1 || rc[0].Provider != "accs" || rc[0].Fallback != "" || spare.calls() != 0 {
		t.Fatalf("recorded %+v", rc)
	}
}

func TestAcceptUnresolvableAndDuplicateFallbacksAreSkipped(t *testing.T) {
	main := jsonUp(429, `{"error":{"message":"limit"}}`)
	spare := jsonUp(0, `{"id":"from-spare","choices":[]}`)
	chain(t, "accd", main, spare, "nowhere/x", "accd/m1", "accdsp/m2", "accdsp/m2")
	rec := send(gateway.New(), "accd/m1")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-spare") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if main.calls() != 1 {
		t.Fatalf("main tried %d times", main.calls())
	}
}

func TestAcceptStreamingSuccessPassesThrough(t *testing.T) {
	main := jsonUp(529, `{"error":{"message":"overloaded"}}`)
	sse := "data: {\"id\":\"s1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
	spare := &upstream{ctype: "text/event-stream", reply: sse}
	chain(t, "accst", main, spare, "accstsp/m2")
	rec := httptest.NewRecorder()
	body := `{"model":"accst/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") || strings.Contains(rec.Body.String(), "overloaded") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
