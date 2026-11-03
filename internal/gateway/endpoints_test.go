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

// scriptReply is what a scripted upstream says on one path.
type scriptReply struct {
	code  int
	ctype string
	body  string
}

// scripted is an upstream that answers each path with its own scriptReply and
// counts what arrived on each.
type scripted struct {
	mu      sync.Mutex
	replies map[string]scriptReply
	calls   map[string]int
}

func (f *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls[r.URL.Path]++
	rep, ok := f.replies[r.URL.Path]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", rep.ctype)
	w.WriteHeader(rep.code)
	io.WriteString(w, rep.body)
}

func (f *scripted) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

const (
	chatPath      = "/v1/chat/completions"
	responsesPath = "/v1/responses"
	messagesPath  = "/v1/messages"
	countPath     = "/v1/messages/count_tokens"
)

// chatOK is a Chat Completions stream answering "from chat".
var chatOK = scriptReply{200, "text/event-stream", sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"from chat"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	`data: [DONE]`)}

// responsesOK is a Responses stream answering "from responses".
var responsesOK = scriptReply{200, "text/event-stream", sse(
	`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from responses"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
)}

func refusal(msg string) scriptReply {
	return scriptReply{400, "application/json", `{"error":{"message":"` + msg + `"}}`}
}

// upstream saves a provider with the given bases (one server for all three
// paths) and the given models, and returns the upstream and a gateway whose
// memory lasts across the requests made through it.
func upstream(t *testing.T, p provider.Provider, replies map[string]scriptReply) (*scripted, http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &scripted{replies: replies, calls: map[string]int{}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p.ID, p.Name, p.Key = "fake", "Fake", "k"
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Responses != "" {
		p.Responses = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return f, New().Handler()
}

func ask(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// A Chat client on a model that only /responses serves: the first request
// goes to /chat/completions, is turned away, and then to /responses; the
// next one goes straight there. Another model of the provider still tries
// Chat first.
func TestChatClientRefusedThenResponses(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1", "m2"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      refusal("This model is not accessible via the /chat/completions endpoint."),
		responsesPath: responsesOK,
	})
	chat := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	code, body := ask(t, h, "/v1/chat/completions", chat)
	if code != 200 || !strings.Contains(body, "from responses") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
	if code, body = ask(t, h, "/v1/chat/completions", chat); code != 200 || !strings.Contains(body, "from responses") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
	// m2 is refused on Chat on its own account, not m1's
	if code, _ = ask(t, h, "/v1/chat/completions", `{"model":"m2","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("m2: %d", code)
	}
	if f.count(chatPath) != 2 {
		t.Fatalf("m2 did not try chat first: chat=%d", f.count(chatPath))
	}
}

// A Responses client on a model that only /chat/completions serves: the
// request is translated to Chat, and once refused on /responses it is not
// sent there again.
func TestResponsesClientRefusedThenChat(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		responsesPath: refusal("unsupported_api_for_model: m1 is not supported"),
		chatPath:      chatOK,
	})
	req := `{"model":"m1","input":"hi"}`
	code, body := ask(t, h, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, "from chat") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(responsesPath) != 1 || f.count(chatPath) != 1 {
		t.Fatalf("first calls: responses=%d chat=%d", f.count(responsesPath), f.count(chatPath))
	}
	if code, body = ask(t, h, "/v1/responses", req); code != 200 || !strings.Contains(body, "from chat") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(responsesPath) != 1 || f.count(chatPath) != 2 {
		t.Fatalf("second calls: responses=%d chat=%d", f.count(responsesPath), f.count(chatPath))
	}
}

// With the model list saying which APIs each model has, a request goes to
// one of them and nowhere else, whatever the agent spoke and whatever else
// the provider has.
func TestDeclaredAPIsAreTheOnlyOnes(t *testing.T) {
	models := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}
	for _, tc := range []struct {
		name, path, body string
		want             string // the one path that may be hit
	}{
		{"anthropic client, gpt-x", "/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, responsesPath},
		{"chat client, gpt-x", "/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, responsesPath},
		{"responses client, claude-x", "/v1/responses", `{"model":"claude-x","input":"hi"}`, chatPath},
		{"anthropic client, claude-x", "/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, chatPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h := upstream(t, provider.Provider{Models: []string{"gpt-x", "claude-x"}, Chat: "x", Responses: "x", Anthropic: "x"}, map[string]scriptReply{
				chatPath: chatOK, responsesPath: responsesOK, messagesPath: chatOK,
			})
			if err := catalog.SaveLive("fake", "x", models); err != nil {
				t.Fatal(err)
			}
			if code, body := ask(t, h, tc.path, tc.body); code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			for _, path := range []string{chatPath, responsesPath, messagesPath} {
				want := 0
				if path == tc.want {
					want = 1
				}
				if got := f.count(path); got != want {
					t.Errorf("%s: %d calls, want %d", path, got, want)
				}
			}
		})
	}
}

// A 5xx that mentions another endpoint is an upstream failure, not a
// refusal: nothing is switched, and the error reaches the agent as it is.
// As the last candidate, a 500 is retried on the same endpoint (retryTimes
// more tries); the point is that no other endpoint is ever asked.
func TestServerErrorDoesNotSwitchEndpoint(t *testing.T) {
	quickRetries(t)
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      {500, "application/json", `{"error":{"message":"use v1/responses"}}`},
		responsesPath: responsesOK,
	})
	code, body := ask(t, h, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count(chatPath) != retryTimes+1 || f.count(responsesPath) != 0 {
		t.Fatalf("calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
}

// A 4xx that is no refusal of the endpoint (a malformed request) is
// the agent's to see, on the first API asked.
func TestOrdinaryClientErrorDoesNotSwitch(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      refusal("messages: field required"),
		responsesPath: responsesOK,
	})
	code, body := ask(t, h, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "field required") {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count(responsesPath) != 0 {
		t.Fatalf("responses was tried %d times", f.count(responsesPath))
	}
}

// With no other API to try, a refusal is the answer, as the upstream gave it.
func TestRefusalWithNoOtherAPIIsReturned(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x"}, map[string]scriptReply{
		chatPath: refusal("not a chat model"),
	})
	code, body := ask(t, h, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "not a chat model") {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count(chatPath) != 1 {
		t.Fatalf("chat calls %d", f.count(chatPath))
	}
}

// count_tokens reaches the upstream only when the model is served on the
// Anthropic API.
func TestCountTokensOnlyWhereAnthropicServes(t *testing.T) {
	body := `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name     string
		declared []string
		want     int
	}{
		{"declared chat only", []string{"chat"}, 0},
		{"declared anthropic", []string{"anthropic"}, 1},
		{"no list", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h := upstream(t, provider.Provider{Models: []string{"claude-x"}, Chat: "x", Anthropic: "x"}, map[string]scriptReply{
				countPath: {200, "application/json", `{"input_tokens":7}`},
			})
			if tc.declared != nil {
				if err := catalog.SaveLive("fake", "x", []catalog.Model{{ID: "claude-x", APIs: tc.declared}}); err != nil {
					t.Fatal(err)
				}
			}
			if code, out := ask(t, h, "/v1/messages/count_tokens", body); code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			if got := f.count(countPath); got != tc.want {
				t.Errorf("upstream count calls %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNotChatModelPhrases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model m1 is not accessible via the /chat/completions endpoint`, true},
		{400, `model m1 is not accessible via the v1/responses endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `use /v1/chat/completions`, true},
		{400, `not supported in the v1/responses`, true},
		{400, `not supported in /v1/responses`, true},
		{404, `use v1/completions`, true},
		{400, `messages: field required`, false},
		{500, `use v1/responses`, false},
		{502, `unsupported_api_for_model`, false},
		{200, `use v1/responses`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// The order translations are tried in: /responses first for OpenAI's
// models on OpenAI's API or on Copilot, Chat first for everything else.
func TestPreferredAPIs(t *testing.T) {
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	responsesFirst := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	openai := provider.Provider{ID: "openai", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com",
		Account: &provider.Account{Agent: "copilot"}}
	other := provider.Provider{ID: "router", Chat: "https://openrouter.ai/api/v1", Responses: "https://openrouter.ai/api/v1"}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{"openai gpt-6-sol", openai, "gpt-6-sol", responsesFirst},
		{"openai o4-mini", openai, "o4-mini", responsesFirst},
		{"copilot gpt-6-sol", copilot, "gpt-6-sol", responsesFirst},
		{"copilot codex", copilot, "gpt-5-codex", responsesFirst},
		{"copilot claude", copilot, "claude-x", chatFirst},
		{"router openai/gpt-oss-120b", other, "openai/gpt-oss-120b", chatFirst},
		{"router claude", other, "claude-x", chatFirst},
	} {
		if got := preferred(tc.p, tc.model); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
