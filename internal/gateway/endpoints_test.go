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

// multiFake serves all three APIs, each answering by a script, and counts
// what reached it.
type multiFake struct {
	mu     sync.Mutex
	hits   map[string]int
	reject map[string]string // path → 4xx body; "" serves
	status map[string]int
}

func (f *multiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[r.URL.Path]++
	rej, bad := f.reject[r.URL.Path]
	code := f.status[r.URL.Path]
	f.mu.Unlock()
	if bad {
		if code == 0 {
			code = 400
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, rej)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v1/chat/completions":
		io.WriteString(w, sse(`data: {"id":"c1","choices":[{"delta":{"content":"from chat"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	case "/v1/responses":
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
			`data: {"type":"response.output_text.delta","delta":"from responses"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
	case "/v1/messages":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"a1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"from anthropic"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *multiFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func multiSetup(t *testing.T, f *multiFake, id string, models ...catalog.Model) (*httptest.Server, http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if len(models) > 0 {
		if err := catalog.SaveLive(id, up.URL, models); err != nil {
			t.Fatal(err)
		}
	}
	return up, New().Handler()
}

func send(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const chatReq = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`

func TestChatRejectedSwitchesToResponsesAndRemembers(t *testing.T) {
	f := &multiFake{reject: map[string]string{"/v1/chat/completions": `{"error":{"message":"model gpt-x is not accessible via the /chat/completions endpoint"}}`}}
	_, h := multiSetup(t, f, "fake")
	body := strings.Replace(chatReq, "%s", "fake/gpt-x", 1)
	for i := 0; i < 2; i++ {
		code, out := send(h, "/v1/chat/completions", body)
		if code != 200 || !strings.Contains(out, "from responses") {
			t.Fatalf("call %d: %d %s", i, code, out)
		}
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 2 {
		t.Fatalf("chat=%d responses=%d", f.count("/v1/chat/completions"), f.count("/v1/responses"))
	}
	// another model of the same provider is not affected
	send(h, "/v1/chat/completions", strings.Replace(chatReq, "%s", "fake/other", 1))
	if f.count("/v1/chat/completions") != 2 {
		t.Fatalf("other model should still try chat: %d", f.count("/v1/chat/completions"))
	}
}

func TestResponsesRejectedSwitchesToChat(t *testing.T) {
	f := &multiFake{reject: map[string]string{"/v1/responses": `{"error":{"code":"unsupported_api_for_model","message":"nope"}}`}}
	_, h := multiSetup(t, f, "fake")
	for i := 0; i < 2; i++ {
		code, out := send(h, "/v1/responses", `{"model":"fake/claude-x","input":"hi"}`)
		if code != 200 || !strings.Contains(out, "from chat") {
			t.Fatalf("call %d: %d %s", i, code, out)
		}
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("responses=%d chat=%d", f.count("/v1/responses"), f.count("/v1/chat/completions"))
	}
}

func TestDeclaredAPIsRouteWithoutProbing(t *testing.T) {
	f := &multiFake{}
	_, h := multiSetup(t, f, "fake",
		catalog.Model{ID: "gpt-x", Name: "gpt-x", APIs: []string{"responses"}},
		catalog.Model{ID: "claude-x", Name: "claude-x", APIs: []string{"chat"}})
	cases := []struct{ path, body, want string }{
		{"/v1/messages", `{"model":"fake/gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/chat/completions", `{"model":"fake/gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/responses", `{"model":"fake/claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"/v1/messages", `{"model":"fake/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	}
	for _, c := range cases {
		before := map[string]int{}
		for _, p := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			before[p] = f.count(p)
		}
		if code, out := send(h, c.path, c.body); code != 200 {
			t.Fatalf("%s: %d %s", c.body, code, out)
		}
		for p, n := range before {
			want := n
			if p == c.want {
				want++
			}
			if f.count(p) != want {
				t.Errorf("%s %s: %s hit %d times, want %d", c.path, c.body, p, f.count(p), want)
			}
		}
	}
	// count_tokens stays local for a model Anthropic does not serve
	code, _ := send(h, "/v1/messages/count_tokens", `{"model":"fake/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || f.count("/v1/messages") != 0 {
		t.Errorf("count_tokens: %d, anthropic hits %d", code, f.count("/v1/messages"))
	}
}

func TestServerErrorNeverSwitchesEndpoint(t *testing.T) {
	f := &multiFake{
		reject: map[string]string{"/v1/chat/completions": `{"error":{"message":"use v1/responses"}}`},
		status: map[string]int{"/v1/chat/completions": 500},
	}
	_, h := multiSetup(t, f, "fake")
	code, _ := send(h, "/v1/chat/completions", strings.Replace(chatReq, "%s", "fake/m", 1))
	if code != 500 || f.count("/v1/responses") != 0 {
		t.Fatalf("code %d responses %d", code, f.count("/v1/responses"))
	}
}

func TestNoEndpointLeftReturnsUpstreamError(t *testing.T) {
	msg := `{"error":{"message":"use v1/responses"}}`
	f := &multiFake{reject: map[string]string{"/v1/chat/completions": msg, "/v1/responses": msg}}
	_, h := multiSetup(t, f, "fake")
	code, out := send(h, "/v1/chat/completions", strings.Replace(chatReq, "%s", "fake/m", 1))
	if code != 400 || !strings.Contains(out, "use v1/responses") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestEndpointPreference(t *testing.T) {
	s := New()
	mk := func(host string) provider.Provider {
		return provider.Provider{ID: "p", Chat: "https://" + host + "/v1", Responses: "https://" + host + "/v1"}
	}
	first := func(p provider.Provider, model string) provider.Protocol {
		return s.endpoints(p, model, provider.Anthropic, true)[0]
	}
	for _, m := range []string{"gpt-6-sol", "o4-mini", "claude-x"} {
		if got := first(mk("api.openai.com"), m); got != provider.Responses {
			t.Errorf("openai %s: %s", m, got)
		}
	}
	if got := first(mk("relay.example.com"), "openai/gpt-oss-120b"); got != provider.Chat {
		t.Errorf("gpt-oss: %s", got)
	}
	s.refuse("p", "gpt-6-sol", provider.Responses)
	if got := s.endpoints(mk("api.openai.com"), "gpt-6-sol", provider.Chat, true); len(got) != 1 || got[0] != provider.Chat {
		t.Errorf("after refusal: %v", got)
	}
	if !openAIFirst(provider.Provider{Account: &provider.Account{Agent: "copilot"}}, "openai/o4-mini") ||
		openAIFirst(provider.Provider{Account: &provider.Account{Agent: "copilot"}}, "claude-sonnet-5") {
		t.Error("copilot preference")
	}
}

func TestNotChatModelPhrases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"message":"model \"x\" is not accessible via the /chat/completions endpoint"}}`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `This model is not supported in the v1/responses endpoint`, true},
		{400, `use /v1/chat/completions`, true},
		{500, `use v1/responses`, false},
		{200, `unsupported_api_for_model`, false},
		{400, `messages: field required`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v", tc.status, tc.body, got)
		}
	}
}
