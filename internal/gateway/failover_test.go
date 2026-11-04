package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// failoverUp is a Chat Completions upstream that records the model it was
// asked for and replies with a fixed status and body.
type failoverUp struct {
	code   int
	reply  string
	calls  int
	models []string
}

func (f *failoverUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	b, _ := io.ReadAll(r.Body)
	var m struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &m)
	f.models = append(f.models, m.Model)
	ct := "application/json"
	if strings.HasPrefix(f.reply, "data:") {
		ct = "text/event-stream"
	}
	w.Header().Set("Content-Type", ct)
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

const failoverOKChat = `{"id":"c1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

// failoverSetup starts upstreams for provider a (model m1, fallback b/m2) and
// provider b (model m2).
func failoverSetup(t *testing.T, a, b *failoverUp) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: []string{"b/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func failoverPost(t *testing.T, srv *Server, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const failoverReq = `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackSwitchesOnRetryableErrors(t *testing.T) {
	cases := []struct {
		name  string
		code  int
		reply string
	}{
		{"429", 429, `{"error":{"message":"slow down"}}`},
		{"402", 402, `{"error":{"message":"payment needed"}}`},
		{"529", 529, `{"error":{"message":"busy"}}`},
		{"500", 500, `{"error":{"message":"oops"}}`},
		{"403 chinese quota", 403, `{"error":{"message":"额度已用完"}}`},
		{"400 insufficient balance", 400, `{"error":{"message":"Insufficient balance"}}`},
		{"400 too many requests", 400, `{"error":{"message":"Too many requests"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &failoverUp{code: c.code, reply: c.reply}
			b := &failoverUp{reply: failoverOKChat}
			failoverSetup(t, a, b)
			srv := New()
			code, out := failoverPost(t, srv, failoverReq)
			if code != 200 || !strings.Contains(out, "from B") {
				t.Fatalf("got %d %s; want B's reply", code, out)
			}
			if strings.Contains(out, "error") {
				t.Fatalf("client saw A's error: %s", out)
			}
			if a.calls != 1 || len(b.models) != 1 || b.models[0] != "m2" {
				t.Fatalf("a calls %d, b models %v; want 1 and [m2]", a.calls, b.models)
			}
			rec := srv.Recent()[0]
			if rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: ") {
				t.Fatalf("record provider %q fallback %q", rec.Provider, rec.Fallback)
			}
		})
	}
}

func TestFallbackKeepsRequestErrors(t *testing.T) {
	a := &failoverUp{code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	code, out := failoverPost(t, srv, failoverReq)
	if code != 400 || !strings.Contains(out, "field required") {
		t.Fatalf("got %d %s; want the 400 as it was", code, out)
	}
	if b.calls != 0 {
		t.Fatalf("b was asked %d times for a request error", b.calls)
	}
	if f := srv.Recent()[0].Fallback; f != "" {
		t.Fatalf("fallback %q on a request that did not switch", f)
	}
}

func TestFallbackAllFailReturnsLast(t *testing.T) {
	a := &failoverUp{code: 429, reply: `{"error":{"message":"slow down"}}`}
	b := &failoverUp{code: 503, reply: `{"error":{"message":"b is down"}}`}
	failoverSetup(t, a, b)
	srv := New()
	code, out := failoverPost(t, srv, failoverReq)
	if code != 503 || !strings.Contains(out, "b is down") {
		t.Fatalf("got %d %s; want B's 503", code, out)
	}
}

func TestFallbackRestingProviderGoesLast(t *testing.T) {
	a := &failoverUp{code: 429, reply: `{"error":{"message":"slow down"}}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	if code, _ := failoverPost(t, srv, failoverReq); code != 200 {
		t.Fatalf("first request: %d", code)
	}
	code, out := failoverPost(t, srv, failoverReq)
	if code != 200 || !strings.Contains(out, "from B") {
		t.Fatalf("second request: %d %s", code, out)
	}
	if a.calls != 1 {
		t.Fatalf("a was tried %d times; it should rest after its failure", a.calls)
	}
	if rec := srv.Recent()[0]; rec.Provider != "b" || rec.Fallback != "" {
		t.Fatalf("second record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
}

func TestFallbackStreamingSuccessIsNotSwitched(t *testing.T) {
	a := &failoverUp{reply: "data: " + `{"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"from A"},"finish_reason":null}]}` + "\n\ndata: [DONE]\n\n"}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	code, out := failoverPost(t, srv, `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "from A") {
		t.Fatalf("got %d %s; want A's stream", code, out)
	}
	if b.calls != 0 {
		t.Fatalf("b was asked %d times after a's stream began", b.calls)
	}
}

func TestFallbackRecordOmittedWithoutSwitch(t *testing.T) {
	a := &failoverUp{reply: `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"from A"},"finish_reason":"stop"}]}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	if code, out := failoverPost(t, srv, failoverReq); code != 200 || !strings.Contains(out, "from A") {
		t.Fatalf("got %d %s", code, out)
	}
	rec := srv.Recent()[0]
	if rec.Provider != "a" || rec.Fallback != "" {
		t.Fatalf("record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
	b2, _ := json.Marshal(rec)
	if strings.Contains(string(b2), `"fallback"`) {
		t.Fatalf("fallback key present without a switch: %s", b2)
	}
}
