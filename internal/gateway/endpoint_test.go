package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// epUpstream is a provider with a Chat and a Responses endpoint (and an
// Anthropic one, unused by these tests). It records each path it is asked
// and answers with code and reply where set, else with a stream.
type epUpstream struct {
	mu    sync.Mutex
	paths []string
	code  map[string]int
	reply map[string]string
}

const epChatStream = `data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}

data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]

`

const epResponsesStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"responses ok"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}` + "\n\n"

func (u *epUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path)
	code, reply := u.code[r.URL.Path], u.reply[r.URL.Path]
	u.mu.Unlock()
	if code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, reply)
		return
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, epChatStream)
	case "/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, epResponsesStream)
	default:
		http.NotFound(w, r)
	}
}

// seen returns the paths asked so far, in order.
func (u *epUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...)
}

// epSetup points magpie at a temp config, serves the upstream, and saves a
// provider "fake" with the given live model list (nil: no list).
func epSetup(t *testing.T, models []catalog.Model) *epUpstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &epUpstream{code: map[string]int{}, reply: map[string]string{}}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if models != nil {
		if err := catalog.SaveLive("fake", "", models); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Models: []string{"m1", "gpt-x", "claude-x"},
		Chat:   up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	return u
}

// epCall sends one request through a gateway handler.
func epCall(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func epSame(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

// Acceptance 1: a Chat client's model is refused on /chat/completions; the
// request goes to /responses, and the refusal is remembered.
func TestEndpointChatRefusedMovesToResponses(t *testing.T) {
	u := epSetup(t, nil)
	u.code["/v1/chat/completions"] = 400
	u.reply["/v1/chat/completions"] = `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`
	h := New().Handler()
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

	code, out := epCall(h, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "responses ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if got := u.seen(); !epSame(got, []string{"/v1/chat/completions", "/v1/responses"}) {
		t.Fatalf("first paths: %v", got)
	}
	code, out = epCall(h, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "responses ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if got := u.seen(); !epSame(got, []string{"/v1/chat/completions", "/v1/responses", "/v1/responses"}) {
		t.Fatalf("second paths: %v", got)
	}
}

// Acceptance 2: a Responses client's model is refused on /responses; the
// request goes to /chat/completions, and the refusal is remembered.
func TestEndpointResponsesRefusedMovesToChat(t *testing.T) {
	u := epSetup(t, nil)
	u.code["/v1/responses"] = 400
	u.reply["/v1/responses"] = `{"error":{"code":"unsupported_api_for_model","message":"model m1 is not supported"}}`
	h := New().Handler()
	body := `{"model":"m1","input":"hi"}`

	code, out := epCall(h, "/v1/responses", body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if got := u.seen(); !epSame(got, []string{"/v1/responses", "/v1/chat/completions"}) {
		t.Fatalf("first paths: %v", got)
	}
	code, out = epCall(h, "/v1/responses", body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if got := u.seen(); !epSame(got, []string{"/v1/responses", "/v1/chat/completions", "/v1/chat/completions"}) {
		t.Fatalf("second paths: %v", got)
	}
}

// Acceptance 3: with the model list naming the APIs, each request goes to
// the one API the model is served on, and nothing else is probed. count_tokens
// goes upstream only where the model is served on Anthropic's API.
func TestEndpointDeclaredAPIsOnly(t *testing.T) {
	models := []catalog.Model{{ID: "gpt-x", APIs: []string{"responses"}}, {ID: "claude-x", APIs: []string{"chat"}}}
	cases := []struct {
		name, path, body string
		want             []string
	}{
		{"anthropic client, gpt-x", "/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, []string{"/v1/responses"}},
		{"chat client, gpt-x", "/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, []string{"/v1/responses"}},
		{"responses client, claude-x", "/v1/responses", `{"model":"claude-x","input":"hi"}`, []string{"/v1/chat/completions"}},
		{"anthropic client, claude-x", "/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, []string{"/v1/chat/completions"}},
		{"count_tokens, gpt-x", "/v1/messages/count_tokens", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := epSetup(t, models)
			code, out := epCall(New().Handler(), tc.path, tc.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			if got := u.seen(); !epSame(got, tc.want) {
				t.Fatalf("upstream paths %v, want %v", got, tc.want)
			}
		})
	}
}

// Acceptance 4: a 5xx that mentions another endpoint does not move the
// request to it.
func TestEndpointServerErrorDoesNotMove(t *testing.T) {
	u := epSetup(t, nil)
	u.code["/v1/chat/completions"] = 500
	u.reply["/v1/chat/completions"] = `{"error":{"message":"use v1/responses"}}`
	code, out := epCall(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 {
		t.Fatalf("status %d, want 500: %s", code, out)
	}
	for _, p := range u.seen() {
		if p == "/v1/responses" {
			t.Fatalf("a 500 moved to /responses: %v", u.seen())
		}
	}
}

func TestEndpointRefusedPhrases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `not accessible via the /chat/completions endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `use /v1/chat/completions`, true},
		{400, `not supported in the v1/responses API`, true},
		{404, `not a chat model`, true},
		{400, `messages: field required`, false},
		{400, `上下文超长`, false},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{200, `use v1/responses`, false},
	} {
		if got := endpointRefused(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("endpointRefused(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestEndpointTranslationOrder(t *testing.T) {
	chat := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	resp := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	for _, tc := range []struct {
		name string
		p    provider.Provider
		m    string
		want []provider.Protocol
	}{
		{"openai host", provider.Provider{Chat: "https://api.openai.com/v1"}, "gpt-6-sol", resp},
		{"openai host, o-series", provider.Provider{Chat: "https://api.openai.com/v1"}, "o4-mini", resp},
		{"copilot gpt", provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com"}, "gpt-6-sol", resp},
		{"copilot o-series", provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com"}, "o4-mini", resp},
		{"copilot claude", provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com"}, "claude-sonnet-5-5", chat},
		{"other vendor", provider.Provider{Chat: "https://example.com/v1"}, "openai/gpt-oss-120b", chat},
	} {
		if got := translationOrder(tc.p, tc.m); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: translationOrder = %v, want %v", tc.name, got, tc.want)
		}
	}
}
