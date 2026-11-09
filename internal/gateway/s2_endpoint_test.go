package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// s2Upstream answers each path with a fixed reply and counts the requests
// each path got.
type s2Upstream struct {
	mu      sync.Mutex
	calls   map[string]int
	replies map[string]s2Reply
}

type s2Reply struct {
	code int // 0 is 200
	body string
}

func (u *s2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls[r.URL.Path]++
	rep, ok := u.replies[r.URL.Path]
	u.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(rep.body, "data:") || strings.HasPrefix(rep.body, "event:") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if rep.code != 0 {
		w.WriteHeader(rep.code)
	}
	io.WriteString(w, rep.body)
}

func (u *s2Upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[path]
}

// s2Setup points magpie at one provider "fake" speaking the given protocols,
// with models declared as the vendor's list says (nil: undeclared).
func s2Setup(t *testing.T, protos []provider.Protocol, declared []catalog.Model, replies map[string]s2Reply) *s2Upstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &s2Upstream{calls: map[string]int{}, replies: replies}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "m2", "gpt-x", "claude-x"}}
	for _, pr := range protos {
		switch pr {
		case provider.Chat:
			p.Chat = up.URL + "/v1"
		case provider.Responses:
			p.Responses = up.URL + "/v1"
		case provider.Anthropic:
			p.Anthropic = up.URL
		}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if declared != nil {
		if err := catalog.SaveLive("fake", up.URL, declared); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func s2Post(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const s2ChatRefused = `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`

const s2ChatSSE = `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"chat ok"},"finish_reason":null}]}

data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]

`

var s2ResponsesSSE = sse(
	`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
)

// (a) Chat client, model refused on chat: answered on responses, and the
// refusal is remembered for that model only.
func TestS2ChatClientMovesToResponsesAndRemembers(t *testing.T) {
	u := s2Setup(t, []provider.Protocol{provider.Chat, provider.Responses}, nil, map[string]s2Reply{
		"/v1/chat/completions": {400, s2ChatRefused},
		"/v1/responses":        {0, s2ResponsesSSE},
	})
	h := New().Handler()
	chatReq := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	code, body := s2Post(t, h, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("first: %d %s", code, body)
	}
	if strings.Contains(body, "not accessible") {
		t.Errorf("the refused reply leaked to the agent: %s", body)
	}
	if c, r := u.count("/v1/chat/completions"), u.count("/v1/responses"); c != 1 || r != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", c, r)
	}
	code, body = s2Post(t, h, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if c, r := u.count("/v1/chat/completions"), u.count("/v1/responses"); c != 1 || r != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", c, r)
	}
	// another model of the provider still tries chat first
	code, body = s2Post(t, h, "/v1/chat/completions", `{"model":"fake/m2","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("other model: %d %s", code, body)
	}
	if c, r := u.count("/v1/chat/completions"), u.count("/v1/responses"); c != 2 || r != 3 {
		t.Fatalf("other model calls: chat=%d responses=%d", c, r)
	}
}

// (b) Responses client, model refused on responses: answered on chat, and
// later requests go straight to chat.
func TestS2ResponsesClientMovesToChatAndRemembers(t *testing.T) {
	u := s2Setup(t, []provider.Protocol{provider.Responses, provider.Chat}, nil, map[string]s2Reply{
		"/v1/responses":        {400, `{"error":{"code":"unsupported_api_for_model","message":"model m1 is not supported"}}`},
		"/v1/chat/completions": {0, s2ChatSSE},
	})
	h := New().Handler()
	req := `{"model":"fake/m1","input":"hi"}`
	code, body := s2Post(t, h, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("first: %d %s", code, body)
	}
	if r, c := u.count("/v1/responses"), u.count("/v1/chat/completions"); r != 1 || c != 1 {
		t.Fatalf("first calls: responses=%d chat=%d", r, c)
	}
	code, body = s2Post(t, h, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if r, c := u.count("/v1/responses"), u.count("/v1/chat/completions"); r != 1 || c != 2 {
		t.Fatalf("second calls: responses=%d chat=%d", r, c)
	}
}

// (c) A model list that names a model's APIs is the whole story: each request
// goes to that one API, with no probe of the others.
func TestS2DeclaredAPIsAreTheOnlyOnes(t *testing.T) {
	declared := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}
	u := s2Setup(t, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}, declared, map[string]s2Reply{
		"/v1/chat/completions": {0, s2ChatSSE},
		"/v1/responses":        {0, s2ResponsesSSE},
	})
	h := New().Handler()
	steps := []struct {
		name, path, body string
		chat, responses  int // expected upstream calls after this step
	}{
		{"anthropic client, gpt-x", "/v1/messages", `{"model":"fake/gpt-x","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, 0, 1},
		{"chat client, gpt-x", "/v1/chat/completions", `{"model":"fake/gpt-x","messages":[{"role":"user","content":"hi"}]}`, 0, 2},
		{"responses client, claude-x", "/v1/responses", `{"model":"fake/claude-x","input":"hi"}`, 1, 2},
		{"anthropic client, claude-x", "/v1/messages", `{"model":"fake/claude-x","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, 2, 2},
	}
	for _, st := range steps {
		code, body := s2Post(t, h, st.path, st.body)
		if code != 200 {
			t.Fatalf("%s: %d %s", st.name, code, body)
		}
		if c, r := u.count("/v1/chat/completions"), u.count("/v1/responses"); c != st.chat || r != st.responses {
			t.Fatalf("%s: calls chat=%d responses=%d, want chat=%d responses=%d", st.name, c, r, st.chat, st.responses)
		}
		if n := u.count("/v1/messages"); n != 0 {
			t.Fatalf("%s: probed the anthropic endpoint %d times", st.name, n)
		}
	}
}

// (d) A 5xx, or a 4xx that does not say the endpoint is wrong, is answered as
// it is: no other endpoint is tried.
func TestS2OtherErrorsDoNotSwitchEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
		chat int
		resp int
		want int
	}{
		// a sole provider's 500 is asked again twice (the retry rule), but
		// the refusal wording never sends it to responses
		{"500 saying use v1/responses", 500, `{"error":{"message":"use v1/responses"}}`, 3, 0, 500},
		{"400 without a refusal", 400, `{"error":{"message":"messages: field required"}}`, 1, 0, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := s2Setup(t, []provider.Protocol{provider.Chat, provider.Responses}, nil, map[string]s2Reply{
				"/v1/chat/completions": {tc.code, tc.msg},
				"/v1/responses":        {0, s2ResponsesSSE},
			})
			code, body := s2Post(t, New().Handler(), "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
			if code != tc.want {
				t.Fatalf("status %d, want %d: %s", code, tc.want, body)
			}
			if c, r := u.count("/v1/chat/completions"), u.count("/v1/responses"); c != tc.chat || r != tc.resp {
				t.Fatalf("calls chat=%d responses=%d", c, r)
			}
		})
	}
}

// count_tokens goes to Anthropic only for a model served there.
func TestS2CountTokensOnlyWhereServed(t *testing.T) {
	declared := []catalog.Model{{ID: "gpt-x", APIs: []string{"responses"}}}
	u := s2Setup(t, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}, declared, map[string]s2Reply{})
	code, body := s2Post(t, New().Handler(), "/v1/messages/count_tokens", `{"model":"fake/gpt-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", code, body)
	}
	if n := u.count("/v1/messages/count_tokens"); n != 0 {
		t.Fatalf("count_tokens forwarded %d times for a model not served on anthropic", n)
	}
}
