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

// epFake serves all three endpoints; each path has a scripted rejection or
// the success reply for its protocol.
type epFake struct {
	mu     sync.Mutex
	hits   []string
	reject map[string][2]string // path -> {status, message}
}

const (
	chatOK = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	respOK = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"m\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	antOK = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"a1\",\"model\":\"m\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

func (f *epFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.Path)
	rj, bad := f.reject[r.URL.Path]
	f.mu.Unlock()
	if bad {
		w.Header().Set("Content-Type", "application/json")
		code := 400
		if rj[0] != "" {
			code = int(rj[0][0]-'0')*100 + int(rj[0][1]-'0')*10 + int(rj[0][2]-'0')
		}
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"`+rj[1]+`"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v1/chat/completions":
		io.WriteString(w, chatOK)
	case "/v1/responses":
		io.WriteString(w, respOK)
	case "/v1/messages":
		io.WriteString(w, antOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *epFake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func epSetup(t *testing.T, id string, f *epFake, anthropic bool, models ...catalog.Model) http.Handler {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: "Fake", Key: "k", Chat: up.URL + "/v1", Responses: up.URL + "/v1"}
	if anthropic {
		p.Anthropic = up.URL
	}
	for _, m := range models {
		p.Models = append(p.Models, m.ID)
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if len(models) > 0 {
		if err := catalog.SaveLive(id, up.URL, models); err != nil {
			t.Fatal(err)
		}
	}
	return New().Handler()
}

func epPost(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const (
	chatBody = `{"model":"fake/%s","messages":[{"role":"user","content":"hi"}]}`
	respBody = `{"model":"fake/%s","input":"hi"}`
	antBody  = `{"model":"fake/%s","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
)

func with(tpl, m string) string { return strings.Replace(tpl, "%s", m, 1) }

func TestRejectedChatSwitchesToResponsesAndIsRemembered(t *testing.T) {
	f := &epFake{reject: map[string][2]string{"/v1/chat/completions": {"", "model is not accessible via the /chat/completions endpoint"}}}
	h := epSetup(t, "fake", f, false)
	for i := 0; i < 2; i++ {
		if code, body := epPost(h, "/v1/chat/completions", with(chatBody, "m1")); code != 200 || !strings.Contains(body, "hello") {
			t.Fatalf("call %d: %d %s", i, code, body)
		}
	}
	if want := []string{"/v1/chat/completions", "/v1/responses", "/v1/responses"}; !reflect.DeepEqual(f.paths(), want) {
		t.Fatalf("hits %v want %v", f.paths(), want)
	}
	// another model of the same provider is not affected
	f.hits = nil
	epPost(h, "/v1/chat/completions", with(chatBody, "m2"))
	if p := f.paths(); len(p) < 1 || p[0] != "/v1/chat/completions" {
		t.Fatalf("other model: %v", p)
	}
}

func TestRejectedResponsesSwitchesToChat(t *testing.T) {
	f := &epFake{reject: map[string][2]string{"/v1/responses": {"", `{\"code\":\"unsupported_api_for_model\"}`}}}
	h := epSetup(t, "fake", f, false)
	for i := 0; i < 2; i++ {
		if code, body := epPost(h, "/v1/responses", with(respBody, "m1")); code != 200 || !strings.Contains(body, "hello") {
			t.Fatalf("call %d: %d %s", i, code, body)
		}
	}
	if want := []string{"/v1/responses", "/v1/chat/completions", "/v1/chat/completions"}; !reflect.DeepEqual(f.paths(), want) {
		t.Fatalf("hits %v want %v", f.paths(), want)
	}
}

func TestDeclaredAPIsNoProbe(t *testing.T) {
	f := &epFake{}
	h := epSetup(t, "fake", f, true,
		catalog.Model{ID: "gpt-x", APIs: []string{"responses"}},
		catalog.Model{ID: "claude-x", APIs: []string{"chat"}})
	for _, tc := range []struct{ name, path, body, want string }{
		{"anthropic->gpt", "/v1/messages", with(antBody, "gpt-x"), "/v1/responses"},
		{"chat->gpt", "/v1/chat/completions", with(chatBody, "gpt-x"), "/v1/responses"},
		{"responses->claude", "/v1/responses", with(respBody, "claude-x"), "/v1/chat/completions"},
		{"anthropic->claude", "/v1/messages", with(antBody, "claude-x"), "/v1/chat/completions"},
	} {
		f.mu.Lock()
		f.hits = nil
		f.mu.Unlock()
		if code, body := epPost(h, tc.path, tc.body); code != 200 {
			t.Fatalf("%s: %d %s", tc.name, code, body)
		}
		if got := f.paths(); !reflect.DeepEqual(got, []string{tc.want}) {
			t.Errorf("%s: hits %v want [%s]", tc.name, got, tc.want)
		}
	}
}

func TestNonRejectionsDoNotSwitch(t *testing.T) {
	for _, tc := range []struct {
		code string
		msg  string
	}{{"500", "use v1/responses"}, {"429", "not a chat model"}} {
		f := &epFake{reject: map[string][2]string{"/v1/chat/completions": {tc.code, tc.msg}}}
		h := epSetup(t, "fake", f, false)
		code, _ := epPost(h, "/v1/chat/completions", with(chatBody, "m1"))
		if code != int(tc.code[0]-'0')*100+int(tc.code[1]-'0')*10+int(tc.code[2]-'0') {
			t.Errorf("%s: status %d", tc.code, code)
		}
		for _, p := range f.paths() {
			if p == "/v1/responses" {
				t.Errorf("%s switched endpoints: %v", tc.code, f.paths())
			}
		}
	}
	if endpointRejected(200, []byte("use v1/responses")) || endpointRejected(503, []byte("use v1/responses")) {
		t.Error("5xx/200 must not reject")
	}
}

func TestRejectedEverywhereReturnsLastError(t *testing.T) {
	f := &epFake{reject: map[string][2]string{
		"/v1/chat/completions": {"", "not accessible via the /chat/completions endpoint"},
		"/v1/responses":        {"404", "not accessible via the /responses endpoint"},
	}}
	h := epSetup(t, "fake", f, false)
	code, body := epPost(h, "/v1/chat/completions", with(chatBody, "m1"))
	if code != 404 || !strings.Contains(body, "/responses endpoint") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestEndpointPreference(t *testing.T) {
	s := New()
	mk := func(id, base string) provider.Provider {
		return provider.Provider{ID: id, Name: id, Key: "k", Chat: base, Responses: base}
	}
	oa := mk("openai", "https://api.openai.com/v1")
	cp := mk("copilot", "https://api.githubcopilot.com")
	cp.Anthropic = "https://api.githubcopilot.com"
	other := mk("other", "https://example.com/v1")
	gem := provider.Gemini
	resp := []provider.Protocol{provider.Responses, provider.Chat}
	chat := []provider.Protocol{provider.Chat, provider.Responses}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{oa, "gpt-6-sol", resp}, {oa, "o4-mini", resp}, {cp, "gpt-6-sol", append(resp, provider.Anthropic)},
		{cp, "o4-mini", append(resp, provider.Anthropic)}, {cp, "openai/gpt-5", append(resp, provider.Anthropic)},
		{cp, "claude-sonnet-4", append(chat, provider.Anthropic)}, {other, "gpt-6-sol", chat}, {other, "openai/gpt-oss-120b", chat},
		{cp, "gpt-oss-120b", append(chat, provider.Anthropic)},
	} {
		if got := s.routeAPIs(tc.p, tc.model, gem); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s/%s: %v want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
	s.markNoAPI("other", "m", provider.Chat)
	if got := s.routeAPIs(other, "m", gem); !reflect.DeepEqual(got, []provider.Protocol{provider.Responses}) {
		t.Errorf("after reject: %v", got)
	}
	if got := s.routeAPIs(other, "m2", gem); !reflect.DeepEqual(got, chat) {
		t.Errorf("other model: %v", got)
	}
}
