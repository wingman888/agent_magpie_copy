package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// scripted is one upstream reply: status, content type, body.
type scripted struct {
	code  int
	ctype string
	body  string
}

// apiFake is an upstream that answers per path and records the paths asked,
// in order, so a test can see exactly which endpoints were tried.
type apiFake struct {
	replies map[string]scripted
	paths   []string
}

func (f *apiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.paths = append(f.paths, r.URL.Path)
	sc, ok := f.replies[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", sc.ctype)
	if sc.code != 0 {
		w.WriteHeader(sc.code)
	}
	io.WriteString(w, sc.body)
}

// chatOK is a Chat Completions stream answering "chat ok".
var chatOK = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	`data: [DONE]`)

// responsesOK is a Responses stream answering "fallback ok".
var responsesOK = sse(
	`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"fallback ok"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`)

// isolateEndpoints keeps the provider and model-list files out of the
// user's real config and cache.
func isolateEndpoints(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// serveTo sends one request to the gateway and returns status and body.
func serveTo(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestChatClientMovesToResponsesAndRemembers(t *testing.T) {
	isolateEndpoints(t)
	f := &apiFake{replies: map[string]scripted{
		"/v1/chat/completions": {400, "application/json", `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`},
		"/v1/responses":        {0, "text/event-stream", responsesOK},
	}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	chat := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	code, body := serveTo(t, s, "/v1/chat/completions", chat)
	if code != 200 || !strings.Contains(body, "fallback ok") {
		t.Fatalf("first: %d %s", code, body)
	}
	if strings.Contains(body, "not accessible") {
		t.Fatalf("the refusal reached the agent: %s", body)
	}
	if want := []string{"/v1/chat/completions", "/v1/responses"}; !reflect.DeepEqual(f.paths, want) {
		t.Fatalf("first request asked %v, want %v", f.paths, want)
	}
	f.paths = nil
	if code, body := serveTo(t, s, "/v1/chat/completions", chat); code != 200 || !strings.Contains(body, "fallback ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if want := []string{"/v1/responses"}; !reflect.DeepEqual(f.paths, want) {
		t.Fatalf("second request asked %v, want only %v", f.paths, want)
	}
}

func TestResponsesClientMovesToChat(t *testing.T) {
	isolateEndpoints(t)
	f := &apiFake{replies: map[string]scripted{
		"/v1/responses":        {400, "application/json", `{"error":{"code":"unsupported_api_for_model","message":"model m1 is not supported"}}`},
		"/v1/chat/completions": {0, "text/event-stream", chatOK},
	}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	req := `{"model":"m1","input":"hi"}`
	code, body := serveTo(t, s, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, "chat ok") || strings.Contains(body, "unsupported_api") {
		t.Fatalf("first: %d %s", code, body)
	}
	if want := []string{"/v1/responses", "/v1/chat/completions"}; !reflect.DeepEqual(f.paths, want) {
		t.Fatalf("first request asked %v, want %v", f.paths, want)
	}
	f.paths = nil
	if code, body := serveTo(t, s, "/v1/responses", req); code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if want := []string{"/v1/chat/completions"}; !reflect.DeepEqual(f.paths, want) {
		t.Fatalf("second request asked %v, want only %v", f.paths, want)
	}
}

// A model the vendor's list declares for one API is sent to that API alone,
// whichever API the agent speaks, and nothing is probed.
func TestDeclaredAPIsAreTheOnlyEndpoints(t *testing.T) {
	isolateEndpoints(t)
	f := &apiFake{replies: map[string]scripted{
		"/v1/responses":        {0, "text/event-stream", responsesOK},
		"/v1/chat/completions": {0, "text/event-stream", chatOK},
		"/v1/messages":         {0, "text/event-stream", chatOK},
	}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := catalog.SaveLive("fake", up.URL, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"gpt-x", "claude-x"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	anthropic := func(model string) string {
		return `{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	}
	chat := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	}
	responses := func(model string) string {
		return `{"model":"` + model + `","input":"hi"}`
	}
	for _, tc := range []struct {
		name, path, body, want string
	}{
		{"anthropic client, responses-only model", "/v1/messages", anthropic("gpt-x"), "/v1/responses"},
		{"chat client, responses-only model", "/v1/chat/completions", chat("gpt-x"), "/v1/responses"},
		{"responses client, chat-only model", "/v1/responses", responses("claude-x"), "/v1/chat/completions"},
		{"anthropic client, chat-only model", "/v1/messages", anthropic("claude-x"), "/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.paths = nil
			code, body := serveTo(t, s, tc.path, tc.body)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			if !reflect.DeepEqual(f.paths, []string{tc.want}) {
				t.Fatalf("upstream asked %v, want exactly [%s]", f.paths, tc.want)
			}
		})
	}
}

// A 5xx that names another API is the provider failing, not the endpoint
// being wrong: the request is not moved to another endpoint.
func TestServerErrorDoesNotMoveEndpoint(t *testing.T) {
	isolateEndpoints(t)
	f := &apiFake{replies: map[string]scripted{
		"/v1/chat/completions": {500, "application/json", `{"error":{"message":"use v1/responses"}}`},
		"/v1/responses":        {0, "text/event-stream", responsesOK},
	}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.sleep = func(context.Context, time.Duration) bool { return true } // the retries do not wait here
	code, body := serveTo(t, s, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 || !strings.Contains(body, "use v1/responses") {
		t.Fatalf("%d %s", code, body)
	}
	// a lone candidate's 500 is tried again (three tries in all), each on
	// the same endpoint: it is never moved to the Responses one
	if want := []string{"/v1/chat/completions", "/v1/chat/completions", "/v1/chat/completions"}; !reflect.DeepEqual(f.paths, want) {
		t.Fatalf("asked %v, want %v", f.paths, want)
	}
}

// The endpoints a model is tried on, in order, for one provider.
func orderOf(t *testing.T, p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	t.Helper()
	isolateEndpoints(t)
	return New().endpoints(p, model, from, relay)
}

// Gemini is a client API no provider here speaks, so every endpoint is a
// translation and the preference alone orders them.
func TestTranslationPreference(t *testing.T) {
	openai := provider.Provider{ID: "openai", Name: "OpenAI", Key: "k",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Name: "Copilot", Key: "k",
		Chat: "https://copilot.test", Responses: "https://copilot.test", Anthropic: "https://copilot.test",
		Account: &provider.Account{Agent: "copilot"}}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k",
		Chat: "https://other.test/v1", Responses: "https://other.test/v1"}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		from  provider.Protocol
		want  []provider.Protocol
	}{
		{"openai api gpt is responses first", openai, "gpt-6-sol", provider.Gemini, []provider.Protocol{provider.Responses, provider.Chat}},
		{"openai api o-series is responses first", openai, "o4-mini", provider.Anthropic, []provider.Protocol{provider.Responses, provider.Chat}},
		{"copilot gpt is responses first", copilot, "gpt-6-sol", provider.Gemini, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{"copilot claude is chat first", copilot, "claude-x", provider.Gemini, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{"copilot claude, responses client, translated after the pass-through", copilot, "claude-x", provider.Responses, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{"copilot claude passes anthropic through", copilot, "claude-x", provider.Anthropic, []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses}},
		{"other vendor is chat first, openai-prefixed included", other, "openai/gpt-oss-120b", provider.Gemini, []provider.Protocol{provider.Chat, provider.Responses}},
		{"other vendor passes responses through", other, "openai/gpt-oss-120b", provider.Responses, []provider.Protocol{provider.Responses, provider.Chat}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderOf(t, tc.p, tc.model, tc.from, true); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeclaredAPIsNarrowTheOrder(t *testing.T) {
	isolateEndpoints(t)
	if err := catalog.SaveLive("fake", "https://fake.test", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "https://fake.test/v1", Responses: "https://fake.test/v1", Anthropic: "https://fake.test"}
	if got := New().endpoints(p, "gpt-x", provider.Chat, true); !reflect.DeepEqual(got, []provider.Protocol{provider.Responses}) {
		t.Fatalf("declared only responses: %v", got)
	}
	// a declared API the provider has no base for leaves nothing to send to
	q := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "https://fake.test/v1"}
	if got := New().endpoints(q, "gpt-x", provider.Chat, true); len(got) != 0 {
		t.Fatalf("declared but unconfigured: %v", got)
	}
	// a model the list says nothing about may use any endpoint
	if got := New().endpoints(p, "m1", provider.Chat, true); len(got) != 3 {
		t.Fatalf("undeclared model: %v", got)
	}
}

func TestRefusalIsPerProviderModelEndpoint(t *testing.T) {
	isolateEndpoints(t)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "https://fake.test/v1", Responses: "https://fake.test/v1"}
	s := New()
	s.markRefused("fake", "m1", provider.Chat)
	if got := s.endpoints(p, "m1", provider.Chat, true); !reflect.DeepEqual(got, []provider.Protocol{provider.Responses}) {
		t.Fatalf("refused model: %v", got)
	}
	if got := s.endpoints(p, "m2", provider.Chat, true); !reflect.DeepEqual(got, []provider.Protocol{provider.Chat, provider.Responses}) {
		t.Fatalf("other model: %v", got)
	}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k",
		Chat: "https://other.test/v1", Responses: "https://other.test/v1"}
	if got := s.endpoints(other, "m1", provider.Chat, true); len(got) != 2 {
		t.Fatalf("other provider: %v", got)
	}
	// refused on every endpoint, the model is still tried: nothing else is left
	s.markRefused("fake", "m1", provider.Responses)
	if got := s.endpoints(p, "m1", provider.Chat, true); len(got) != 2 {
		t.Fatalf("all refused: %v", got)
	}
	s.clearRefused("fake", "m1", provider.Chat)
	if got := s.endpoints(p, "m1", provider.Chat, true); !reflect.DeepEqual(got, []provider.Protocol{provider.Chat}) {
		t.Fatalf("cleared: %v", got)
	}
}

func TestNotServedHere(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model m1 is not accessible via the /chat/completions endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `This model is not supported in the v1/responses endpoint`, true},
		{400, `This model is not supported in the v1/chat/completions endpoint`, true},
		{400, `use /v1/chat/completions`, true},
		{404, `use v1/responses`, true},
		{500, `use v1/responses`, false},
		{502, `not a chat model`, false},
		{200, `use v1/responses`, false},
		{400, `messages: field required`, false},
		{400, `上下文超长`, false},
		{400, `模型不存在`, false},
		{429, `rate limit`, false},
	} {
		if got := notServedHere(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notServedHere(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}
