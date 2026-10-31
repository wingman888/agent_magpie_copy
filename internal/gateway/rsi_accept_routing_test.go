package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func acceptEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func acceptSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

type acceptReply struct {
	code  int
	ctype string
	body  string
	hdr   map[string]string
}

// acceptUp answers with the next scripted reply (the last one repeatedly),
// counting hits per path.
type acceptUp struct {
	mu      sync.Mutex
	replies []acceptReply
	n       int
	paths   map[string]int
	hang    chan struct{}
}

func (s *acceptUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	i := min(s.n, len(s.replies)-1)
	s.n++
	if s.paths == nil {
		s.paths = map[string]int{}
	}
	s.paths[r.URL.Path]++
	hang := s.hang
	s.mu.Unlock()
	if hang != nil && i == 0 {
		select {
		case <-hang:
		case <-r.Context().Done():
			return
		}
	}
	x := s.replies[i]
	if x.ctype == "" {
		x.ctype = "application/json"
	}
	w.Header().Set("Content-Type", x.ctype)
	for k, v := range x.hdr {
		w.Header().Set(k, v)
	}
	if x.code != 0 {
		w.WriteHeader(x.code)
	}
	io.WriteString(w, x.body)
}

func (s *acceptUp) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func acceptAddChat(t *testing.T, id string, s http.Handler) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func acceptAddAnthropic(t *testing.T, id string, s http.Handler) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
}

func acceptGroup(t *testing.T, name string, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: name, Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func acceptPost(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const acceptChatOK = `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
const acceptChatBody = `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`

// Acceptance 1 + 4.
func TestAcceptChatClientSwitchesToResponses(t *testing.T) {
	acceptEnv(t)
	var mu sync.Mutex
	var chat, resp int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			chat++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"code":null,"message":"model \"m1\" is not accessible via the /chat/completions endpoint","type":"invalid_request_error"}`)
		case "/v1/responses":
			resp++
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, acceptSSE(
				`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
				`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"fallback ok"}`,
				`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "acc1", Name: "Acc1", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := gateway.New().Handler()
	for i, want := range []int{1, 2} {
		code, body := acceptPost(h, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`)
		if code != 200 || !strings.Contains(body, "fallback ok") {
			t.Fatalf("call %d: %d %s", i, code, body)
		}
		mu.Lock()
		c, r := chat, resp
		mu.Unlock()
		if c != 1 || r != want {
			t.Fatalf("call %d: chat=%d responses=%d", i, c, r)
		}
	}
}

// Acceptance 2.
func TestAcceptResponsesClientSwitchesToChat(t *testing.T) {
	acceptEnv(t)
	var mu sync.Mutex
	var chat, resp int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/responses":
			resp++
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"model \"claude-x\" is not accessible via the /responses endpoint","code":"unsupported_api_for_model"}}`)
		case "/v1/chat/completions":
			chat++
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, acceptSSE(
				`data: {"id":"c1","model":"claude-x","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
				`data: [DONE]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "acc2", Name: "Acc2", Key: "k", Models: []string{"claude-x"}, Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := gateway.New().Handler()
	for i, want := range []int{1, 2} {
		code, body := acceptPost(h, "/v1/responses", `{"model":"claude-x","input":"hi","stream":true}`)
		if code != 200 || !strings.Contains(body, "chat ok") {
			t.Fatalf("call %d: %d %s", i, code, body)
		}
		mu.Lock()
		c, r := chat, resp
		mu.Unlock()
		if r != 1 || c != want {
			t.Fatalf("call %d: responses=%d chat=%d", i, r, c)
		}
	}
}

// A 5xx saying "use v1/responses" is not a wrong-endpoint signal: Responses
// is never tried and the error comes back.
func TestAcceptServerErrorDoesNotSwitchEndpoint(t *testing.T) {
	acceptEnv(t)
	var mu sync.Mutex
	paths := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"use v1/responses"}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "acc3", Name: "Acc3", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := gateway.New().Handler()
	code, _ := acceptPost(h, "/v1/chat/completions", acceptChatBody)
	mu.Lock()
	defer mu.Unlock()
	if code != 500 || paths["/v1/responses"] != 0 || paths["/v1/chat/completions"] == 0 {
		t.Fatalf("status %d, paths %v", code, paths)
	}
}

// Acceptance 3: the model list says where each model is served.
func TestAcceptModelListPicksEndpoint(t *testing.T) {
	acceptEnv(t)
	var mu sync.Mutex
	paths := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/responses":
			io.WriteString(w, acceptSSE(
				`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"gpt-x","usage":{"input_tokens":0,"output_tokens":0}}}`,
				`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"ok"}`,
				`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"gpt-x","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
		case "/v1/chat/completions":
			io.WriteString(w, acceptSSE(
				`data: {"id":"c1","model":"claude-x","choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`))
		default:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"unexpected"}}`)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "acc4", Name: "Acc4", Key: "k", Models: []string{"gpt-x", "claude-x"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("acc4", up.URL+"/v1", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	// the declared APIs are readable back
	p := provider.Provider{ID: "acc4"}
	if got := p.APIs("gpt-x"); len(got) != 1 || got[0] != provider.Responses {
		t.Errorf("APIs(gpt-x) = %v", got)
	}
	if got := p.APIs("nope"); got != nil {
		t.Errorf("APIs(nope) = %v", got)
	}
	h := gateway.New().Handler()
	for _, tc := range []struct{ path, body, want string }{
		{"/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/responses", `{"model":"claude-x","input":"hi","stream":true}`, "/v1/chat/completions"},
		{"/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	} {
		mu.Lock()
		clear(paths)
		mu.Unlock()
		code, body := acceptPost(h, tc.path, tc.body)
		mu.Lock()
		n, one := len(paths), paths[tc.want]
		mu.Unlock()
		if code != 200 || n != 1 || one != 1 {
			t.Errorf("%s %s: status %d, upstream %v, want only %s: %s", tc.path, tc.body, code, paths, tc.want, body)
		}
	}
}

// Acceptance 5.
func TestAcceptGroupFailsOverOnUnservedModel(t *testing.T) {
	cases := []struct {
		code int
		body string
		over bool
	}{
		{400, `{"error":{"message":"model \"gpt-6-sol\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`, true},
		{401, `{"error":{"message":"invalid api key"}}`, true},
		{404, `{"error":{"message":"not found"}}`, true},
		{400, `{"error":{"message":"模型不存在"}}`, true},
		{400, `{"error":{"message":"The model gpt-x does not exist"}}`, true},
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{400, `{"error":{"message":"Maximum context length is 128000 tokens, you requested 200000. Please reduce the length of the messages."}}`, false},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			acceptEnv(t)
			a := &acceptUp{replies: []acceptReply{{code: tc.code, body: tc.body}}}
			b := &acceptUp{replies: []acceptReply{{code: 200, body: acceptChatOK}}}
			ida, idb := fmt.Sprintf("fa%d", i), fmt.Sprintf("fb%d", i)
			acceptAddChat(t, ida, a)
			acceptAddChat(t, idb, b)
			acceptGroup(t, "G", ida+"/m", idb+"/m")
			code, body := acceptPost(gateway.New().Handler(), "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
			if tc.over {
				if code != 200 || !strings.Contains(body, "hello") || b.count() != 1 {
					t.Fatalf("%d %s (b %d)", code, body, b.count())
				}
			} else if code != tc.code || b.count() != 0 {
				t.Fatalf("%d %s (b %d)", code, body, b.count())
			}
		})
	}
}

// Acceptance 6.
func TestAcceptStreamErrorBeforeContentFailsOver(t *testing.T) {
	good := acceptSSE(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m2","role":"assistant","content":[],"usage":{"input_tokens":3}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	req := `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

	acceptEnv(t)
	early := acceptSSE(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","content":[],"usage":{"input_tokens":3}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	a := &acceptUp{replies: []acceptReply{{200, "text/event-stream", early, nil}}}
	b := &acceptUp{replies: []acceptReply{{200, "text/event-stream", good, nil}}}
	acceptAddAnthropic(t, "sa1", a)
	acceptAddAnthropic(t, "sb1", b)
	acceptGroup(t, "G", "sa1/m", "sb1/m")
	code, body := acceptPost(gateway.New().Handler(), "/v1/messages", req)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "Overloaded") || strings.Contains(body, `"m1"`) {
		t.Fatalf("%d %s", code, body)
	}

	// after content has begun, the error reaches the agent and B is not asked
	acceptEnv(t)
	late := acceptSSE(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","content":[]}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	a = &acceptUp{replies: []acceptReply{{200, "text/event-stream", late, nil}}}
	b = &acceptUp{replies: []acceptReply{{200, "text/event-stream", good, nil}}}
	acceptAddAnthropic(t, "sa2", a)
	acceptAddAnthropic(t, "sb2", b)
	acceptGroup(t, "G", "sa2/m", "sb2/m")
	code, body = acceptPost(gateway.New().Handler(), "/v1/messages", req)
	if code != 200 || !strings.Contains(body, "half") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("late: %d %s (b %d)", code, body, b.count())
	}
}

// Acceptance 7.
func TestAcceptLastCandidateIsRetried(t *testing.T) {
	req := `{"model":"%s/m","messages":[{"role":"user","content":"hi"}]}`

	acceptEnv(t)
	a := &acceptUp{replies: []acceptReply{{503, "", `{"error":{"message":"upstream connect error"}}`, nil}, {502, "", `{"error":{"message":"bad gateway"}}`, nil}, {200, "", acceptChatOK, nil}}}
	acceptAddChat(t, "ra1", a)
	start := time.Now()
	code, body := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "ra1"))
	if code != 200 || !strings.Contains(body, "hello") || a.count() != 3 {
		t.Fatalf("%d %s (tried %d)", code, body, a.count())
	}
	_ = start

	acceptEnv(t)
	a = &acceptUp{replies: []acceptReply{{503, "", `{"error":{"message":"down"}}`, nil}}}
	acceptAddChat(t, "ra2", a)
	if code, _ := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "ra2")); code != 503 || a.count() != 3 {
		t.Fatalf("persistent: %d after %d tries", code, a.count())
	}

	acceptEnv(t)
	a = &acceptUp{replies: []acceptReply{{400, "", `{"error":{"message":"messages: field required"}}`, nil}}}
	acceptAddChat(t, "ra3", a)
	if code, _ := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "ra3")); code != 400 || a.count() != 1 {
		t.Fatalf("bad request: %d after %d tries", code, a.count())
	}
}

// 429 without a recovery time, or with one too long, is not retried; with a
// short Retry-After it is.
func TestAcceptRateLimitRetryRules(t *testing.T) {
	req := `{"model":"%s/m","messages":[{"role":"user","content":"hi"}]}`
	acceptEnv(t)
	a := &acceptUp{replies: []acceptReply{{429, "", `{"error":{"message":"slow down"}}`, nil}}}
	acceptAddChat(t, "rl1", a)
	if code, _ := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "rl1")); code != 429 || a.count() != 1 {
		t.Fatalf("no Retry-After: %d after %d", code, a.count())
	}
	acceptEnv(t)
	a = &acceptUp{replies: []acceptReply{{429, "", `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "60"}}}}
	acceptAddChat(t, "rl2", a)
	if code, _ := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "rl2")); code != 429 || a.count() != 1 {
		t.Fatalf("long Retry-After: %d after %d", code, a.count())
	}
	acceptEnv(t)
	a = &acceptUp{replies: []acceptReply{{429, "", `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "1"}}, {200, "", acceptChatOK, nil}}}
	acceptAddChat(t, "rl3", a)
	if code, body := acceptPost(gateway.New().Handler(), "/v1/chat/completions", fmt.Sprintf(req, "rl3")); code != 200 || !strings.Contains(body, "hello") || a.count() != 2 {
		t.Fatalf("short Retry-After: %d %s after %d", code, body, a.count())
	}
}

// Acceptance 8.
func TestAcceptCanceledRequestRestsNobody(t *testing.T) {
	acceptEnv(t)
	a := &acceptUp{replies: []acceptReply{{200, "", acceptChatOK, nil}}, hang: make(chan struct{})}
	b := &acceptUp{replies: []acceptReply{{200, "", acceptChatOK, nil}}}
	acceptAddChat(t, "ca", a)
	acceptAddChat(t, "cb", b)
	acceptGroup(t, "G", "ca/m", "cb/m")
	h := gateway.New().Handler()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx))
	close(a.hang)
	if b.count() != 0 {
		t.Fatalf("b asked %d times after cancel", b.count())
	}
	// A is not resting: the next request goes to A first.
	code, body := acceptPost(h, "/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.count() != 2 || b.count() != 0 {
		t.Fatalf("after cancel: %d %s (a %d, b %d)", code, body, a.count(), b.count())
	}
}
