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

// reply is what the scripted upstream answers one path with.
type reply struct {
	code  int
	ctype string
	body  string
}

// scripted is an upstream that answers by path and records every call.
type scripted struct {
	mu      sync.Mutex
	replies map[string]reply
	calls   []string
}

func (f *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path)
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

// count is how many calls reached path.
func (f *scripted) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == path {
			n++
		}
	}
	return n
}

const (
	pChat      = "/v1/chat/completions"
	pResponses = "/v1/responses"
	pAnthropic = "/v1/messages"
	pCount     = "/v1/messages/count_tokens"
)

var (
	chatOK = sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)
	responsesOK = sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
	)
)

// endpointSetup points the fake provider "fake" at a scripted upstream over
// the chosen APIs. live, when not nil, is the vendor's declared model list.
func endpointSetup(t *testing.T, f *scripted, models []string, live []catalog.Model, chat, responses, anthropic bool) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: models}
	if chat {
		p.Chat = up.URL + "/v1"
	}
	if responses {
		p.Responses = up.URL + "/v1"
	}
	if anthropic {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if live != nil {
		if err := catalog.SaveLive("fake", up.URL, live); err != nil {
			t.Fatal(err)
		}
	}
}

// callOn posts one request to a handler, so memory it keeps carries over.
func callOn(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// (a) A Chat client whose model is refused on /chat/completions: the request
// goes to /responses and succeeds; the next one goes straight there.
func TestChatClientRefusedOnChatMovesToResponses(t *testing.T) {
	f := &scripted{replies: map[string]reply{
		pChat:      {400, "application/json", `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`},
		pResponses: {200, "text/event-stream", responsesOK},
	}}
	endpointSetup(t, f, []string{"m1"}, nil, true, true, false)
	h := New().Handler()
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

	code, out := callOn(h, pChat, body)
	if code != 200 || !strings.Contains(out, "responses ok") || strings.Contains(out, "not accessible") {
		t.Fatalf("first: status %d: %s", code, out)
	}
	if f.count(pChat) != 1 || f.count(pResponses) != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count(pChat), f.count(pResponses))
	}
	code, out = callOn(h, pChat, body)
	if code != 200 || !strings.Contains(out, "responses ok") {
		t.Fatalf("second: status %d: %s", code, out)
	}
	if f.count(pChat) != 1 || f.count(pResponses) != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count(pChat), f.count(pResponses))
	}
}

// (b) A Responses client whose model /responses turns away as unsupported:
// the request goes to /chat/completions; the next one goes straight there.
func TestResponsesClientRefusedOnResponsesMovesToChat(t *testing.T) {
	f := &scripted{replies: map[string]reply{
		pResponses: {400, "application/json", `{"error":{"code":"unsupported_api_for_model","message":"m1 is not supported"}}`},
		pChat:      {200, "text/event-stream", chatOK},
	}}
	endpointSetup(t, f, []string{"m1"}, nil, true, true, false)
	h := New().Handler()
	body := `{"model":"m1","input":"hi"}`

	code, out := callOn(h, pResponses, body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("first: status %d: %s", code, out)
	}
	if f.count(pResponses) != 1 || f.count(pChat) != 1 {
		t.Fatalf("first calls: responses=%d chat=%d", f.count(pResponses), f.count(pChat))
	}
	code, out = callOn(h, pResponses, body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("second: status %d: %s", code, out)
	}
	if f.count(pResponses) != 1 || f.count(pChat) != 2 {
		t.Fatalf("second calls: responses=%d chat=%d", f.count(pResponses), f.count(pChat))
	}
}

// (c) With the vendor's list declaring gpt-x over responses only and claude-x
// over chat only, each request reaches exactly one endpoint, once, and the
// declared Anthropic endpoint is never probed for claude-x.
func TestDeclaredAPIsAreTheOnlyOnesTried(t *testing.T) {
	live := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}
	cases := []struct {
		name, path, body string
		want             string
	}{
		{"anthropic client, gpt-x", pAnthropic, `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, pResponses},
		{"chat client, gpt-x", pChat, `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, pResponses},
		{"responses client, claude-x", pResponses, `{"model":"claude-x","input":"hi"}`, pChat},
		{"anthropic client, claude-x", pAnthropic, `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, pChat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &scripted{replies: map[string]reply{
				pChat:      {200, "text/event-stream", chatOK},
				pResponses: {200, "text/event-stream", responsesOK},
				pAnthropic: {500, "application/json", `{"error":{"message":"must not be called"}}`},
			}}
			endpointSetup(t, f, []string{"gpt-x", "claude-x"}, live, true, true, true)
			code, out := callOn(New().Handler(), tc.path, tc.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			total := 0
			for _, p := range []string{pChat, pResponses, pAnthropic} {
				total += f.count(p)
			}
			if total != 1 || f.count(tc.want) != 1 {
				t.Fatalf("calls %v, want exactly one to %s", f.calls, tc.want)
			}
		})
	}
}

// (d) A 500 that names another API is a server fault, not a refusal: it is
// delivered as it is, and the other API is not tried.
func TestServerErrorNamingAnotherAPIIsNotRefusal(t *testing.T) {
	f := &scripted{replies: map[string]reply{
		pChat:      {500, "application/json", `{"error":{"message":"upstream broke; use v1/responses"}}`},
		pResponses: {200, "text/event-stream", responsesOK},
	}}
	endpointSetup(t, f, []string{"m1"}, nil, true, true, false)
	h := New().Handler()
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	for i := 1; i <= 2; i++ {
		code, out := callOn(h, pChat, body)
		if code != 500 {
			t.Fatalf("request %d: status %d, want 500: %s", i, code, out)
		}
		if f.count(pResponses) != 0 || f.count(pChat) != i {
			t.Fatalf("request %d calls: chat=%d responses=%d", i, f.count(pChat), f.count(pResponses))
		}
	}
}

// (d2) A plain server fault on the last candidate is still retried: the
// wording check only stops retries for replies that name another API.
func TestPlainServerFaultOnLastCandidateIsRetried(t *testing.T) {
	waits := recordWaits(t)
	f := &scripted{replies: map[string]reply{
		pChat: {500, "application/json", `{"error":{"message":"upstream broke"}}`},
	}}
	endpointSetup(t, f, []string{"m1"}, nil, true, false, false)
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	code, out := callOn(New().Handler(), pChat, body)
	if code != 500 {
		t.Fatalf("status %d, want 500: %s", code, out)
	}
	if f.count(pChat) != 3 || len(*waits) != 2 {
		t.Fatalf("chat calls %d, waits %v; want 3 calls and 2 waits", f.count(pChat), *waits)
	}
}

// endpointRefused tells a refusal of the endpoint from every other failure.
func TestEndpointRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, "model m1 not accessible via the /chat/completions endpoint", true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{404, "use v1/responses", true},
		{400, "This model is not supported in the v1/chat/completions endpoint.", true},
		{400, "not supported in the v1/responses", true},
		{400, "not a chat model", true},
		{400, "use /v1/chat/completions", true},
		{500, "use v1/responses", false},
		{503, "not accessible via the /chat/completions endpoint", false},
		{200, "use v1/responses", false},
		{400, "messages: field required", false},
		{429, "rate limit", false},
	} {
		if got := endpointRefused(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("endpointRefused(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// (e) Protocol preference: OpenAI's own models try Responses first when
// translated; other vendors' models try Chat first, gpt-oss included.
func TestTranslationOrder(t *testing.T) {
	const (
		C = provider.Chat
		R = provider.Responses
		A = provider.Anthropic
	)
	openai := provider.Provider{ID: "openai", Name: "OpenAI", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Name: "Copilot", Chat: "https://api.githubcopilot.com",
		Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com",
		Account: &provider.Account{Agent: "copilot"}}
	other := provider.Provider{ID: "groq", Name: "Groq", Chat: "https://api.groq.com/openai/v1", Responses: "https://api.groq.com/openai/v1"}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{"openai api, gpt", openai, "gpt-6-sol", []provider.Protocol{R, C, A}},
		{"openai api, o4", openai, "o4-mini", []provider.Protocol{R, C, A}},
		{"copilot gpt", copilot, "gpt-6-sol", []provider.Protocol{R, C, A}},
		{"copilot o-series with vendor prefix", copilot, "openai/o4-mini", []provider.Protocol{R, C, A}},
		{"copilot codex", copilot, "codex-mini", []provider.Protocol{R, C, A}},
		{"copilot claude", copilot, "claude-x", []provider.Protocol{C, R, A}},
		{"copilot gpt-oss is not OpenAI's", copilot, "openai/gpt-oss-120b", []provider.Protocol{C, R, A}},
		{"other vendor's gpt-oss", other, "openai/gpt-oss-120b", []provider.Protocol{C, R, A}},
		{"other vendor's gpt name", other, "gpt-6-sol", []provider.Protocol{C, R, A}},
	} {
		if got := translateOrder(tc.p, tc.model); !equalProtos(got, tc.want) {
			t.Errorf("%s: translateOrder = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func equalProtos(a, b []provider.Protocol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// After a refusal, the model keeps only the APIs that weren't refused; the
// memory covers that (provider, model, API) alone.
func TestRefusalIsScopedToProviderModelAndAPI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "fake", Name: "Fake", Chat: "https://a.test/v1",
		Responses: "https://a.test/v1", Anthropic: "https://a.test"}
	other := p
	other.ID = "fake2"
	s := New()
	all := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	check := func(name string, got, want []provider.Protocol) {
		t.Helper()
		if !equalProtos(got, want) {
			t.Errorf("%s: endpoints = %v, want %v", name, got, want)
		}
	}
	check("before", s.endpoints(p, "m1", provider.Chat, true), all)
	s.markRefused("fake", "m1", provider.Chat)
	check("m1 after chat refusal", s.endpoints(p, "m1", provider.Chat, true),
		[]provider.Protocol{provider.Responses, provider.Anthropic})
	check("m1 via anthropic client", s.endpoints(p, "m1", provider.Anthropic, true),
		[]provider.Protocol{provider.Anthropic, provider.Responses})
	check("other model", s.endpoints(p, "m2", provider.Chat, true), all)
	check("other provider", s.endpoints(other, "m1", provider.Chat, true), all)
}

// count_tokens reaches the provider only where the model is served over
// Anthropic's API.
func TestCountTokensOnlyWhereAnthropicServesModel(t *testing.T) {
	f := &scripted{replies: map[string]reply{
		pCount: {200, "application/json", `{"input_tokens":42}`},
	}}
	live := []catalog.Model{
		{ID: "claude-x", APIs: []string{"chat"}},
		{ID: "claude-y", APIs: []string{"anthropic"}},
	}
	endpointSetup(t, f, []string{"claude-x", "claude-y"}, live, true, false, true)
	h := New().Handler()

	code, out := callOn(h, "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || strings.Contains(out, "42") || f.count(pCount) != 0 {
		t.Fatalf("claude-x: status %d, calls %v: %s", code, f.calls, out)
	}
	code, out = callOn(h, "/v1/messages/count_tokens", `{"model":"claude-y","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "42") || f.count(pCount) != 1 {
		t.Fatalf("claude-y: status %d, calls %v: %s", code, f.calls, out)
	}
}
