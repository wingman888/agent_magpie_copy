package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// gktReply is what the fake upstream answers for one key.
type gktReply struct {
	status int
	ctype  string
	body   string
}

// gktUp is a fake upstream that answers by the key it was called with and
// keeps a record of every call as "path key".
type gktUp struct {
	mu    sync.Mutex
	seen  []string
	reply map[string]gktReply
}

func (u *gktUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" "+key)
	rep, ok := u.reply[key]
	u.mu.Unlock()
	if !ok {
		rep = gktReply{status: 500, ctype: "application/json", body: `{"error":"unknown key"}`}
	}
	w.Header().Set("Content-Type", rep.ctype)
	w.WriteHeader(rep.status)
	io.WriteString(w, rep.body)
}

func (u *gktUp) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

const gktChatOK = `{"id":"c1","object":"chat.completion","created":0,"model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

const gktChatSSE = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

const gktAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// gktEnv isolates config and starts a fake upstream.
func gktEnv(t *testing.T, reply map[string]gktReply) (*gktUp, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	u := &gktUp{reply: reply}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	return u, up
}

func gktPost(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const gktChatBody = `{"model":"gkt/m1","messages":[{"role":"user","content":"hi"}]}`

func TestGktSpentKeyFallsToNextKey(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-1": {429, "application/json", `{"error":"rate limited"}`},
		"gk-2": {200, "application/json", gktChatOK},
	})
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt", Chat: up.URL + "/v1",
		Key: "gk-1", KeyName: "personal", Keys: []provider.SubKey{{Name: "team", Key: "gk-2"}}}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	code, body := gktPost(t, gw.Handler(), "/v1/chat/completions", gktChatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.calls(); strings.Join(got, ",") != "/v1/chat/completions gk-1,/v1/chat/completions gk-2" {
		t.Fatalf("upstream calls = %v", got)
	}
	recent := gw.Recent()
	if len(recent) == 0 {
		t.Fatal("no call recorded")
	}
	c := recent[0]
	if c.Provider != "gkt" || c.Status != 200 {
		t.Fatalf("call = provider %q status %d", c.Provider, c.Status)
	}
	if !strings.Contains(c.Fallback, "gkt") || !strings.Contains(c.Fallback, "personal") {
		t.Fatalf("fallback = %q, want the provider and the named key that failed", c.Fallback)
	}

	// the spent key rests: the next request goes straight to the second one
	if code, body := gktPost(t, gw.Handler(), "/v1/chat/completions", gktChatBody); code != 200 {
		t.Fatalf("second request: status %d: %s", code, body)
	}
	got := u.calls()
	if len(got) != 3 || got[2] != "/v1/chat/completions gk-2" {
		t.Fatalf("second request did not start with the second key: %v", got)
	}
	if last := gw.Recent()[0]; last.Fallback != "" {
		t.Fatalf("second request fell back again: %q", last.Fallback)
	}
}

func TestGktDisabledKeyNeverTried(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-1": {429, "application/json", `{"error":"rate limited"}`},
		"gk-2": {200, "application/json", gktChatOK},
	})
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt", Chat: up.URL + "/v1",
		Key: "gk-1", Keys: []provider.SubKey{{Key: "gk-2", Off: true}}}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	code, _ := gktPost(t, gw.Handler(), "/v1/chat/completions", gktChatBody)
	if code != 429 {
		t.Fatalf("status %d, want the rate limit returned as it is", code)
	}
	if got := u.calls(); len(got) != 1 || got[0] != "/v1/chat/completions gk-1" {
		t.Fatalf("a disabled key was tried: %v", got)
	}
	if f := gw.Recent()[0].Fallback; f != "" {
		t.Fatalf("fallback = %q, want none when nothing was retried", f)
	}
}

func TestGktOrdinaryErrorNotRetried(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-1": {400, "application/json", `{"error":{"message":"bad request"}}`},
		"gk-2": {200, "application/json", gktChatOK},
	})
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt", Chat: up.URL + "/v1",
		Key: "gk-1", Keys: []provider.SubKey{{Key: "gk-2"}}}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	code, _ := gktPost(t, gw.Handler(), "/v1/chat/completions", gktChatBody)
	if code != 400 {
		t.Fatalf("status %d, want 400 passed through", code)
	}
	if got := u.calls(); len(got) != 1 {
		t.Fatalf("a 400 was retried on another key: %v", got)
	}
}

func TestGktLastKeysErrorReturned(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-1": {429, "application/json", `{"error":"rate limited"}`},
		"gk-2": {402, "application/json", `{"error":"payment required"}`},
	})
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt", Chat: up.URL + "/v1",
		Key: "gk-1", KeyName: "personal", Keys: []provider.SubKey{{Key: "gk-2"}}}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	code, body := gktPost(t, gw.Handler(), "/v1/chat/completions", gktChatBody)
	if code != 402 {
		t.Fatalf("status %d (%s), want the last key's error", code, body)
	}
	if len(u.calls()) != 2 {
		t.Fatalf("calls = %v, want both keys tried", u.calls())
	}
	if f := gw.Recent()[0].Fallback; !strings.Contains(f, "personal") {
		t.Fatalf("fallback = %q, want the first key named", f)
	}
}

func TestGktKeyProtocolLimitsEndpoint(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-oa": {200, "text/event-stream", gktChatSSE},
		"gk-an": {200, "text/event-stream", gktAnthropicSSE},
	})
	// the current key is OpenAI-only and listed first; the Anthropic key follows
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt",
		Chat: up.URL + "/v1", Anthropic: up.URL,
		Key: "gk-oa", KeyProtocol: provider.Chat,
		Keys: []provider.SubKey{{Key: "gk-an", Protocol: provider.Anthropic}}}); err != nil {
		t.Fatal(err)
	}
	gw := New()

	// a Claude model asked for over Chat goes to the Anthropic endpoint, translated
	code, body := gktPost(t, gw.Handler(), "/v1/chat/completions", `{"model":"gkt/claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("claude over chat: status %d: %s", code, body)
	}
	if got := u.calls(); len(got) != 1 || got[0] != "/v1/messages gk-an" {
		t.Fatalf("claude over chat calls = %v, want the Anthropic key on /v1/messages", got)
	}

	// a GPT model asked for over Anthropic goes to the OpenAI endpoint, translated
	code, body = gktPost(t, gw.Handler(), "/v1/messages", `{"model":"gkt/gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("gpt over anthropic: status %d: %s", code, body)
	}
	got := u.calls()
	if len(got) != 2 || got[1] != "/v1/chat/completions gk-oa" {
		t.Fatalf("gpt over anthropic calls = %v, want the OpenAI key on /v1/chat/completions", got)
	}
}

func TestGktCountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	u, up := gktEnv(t, map[string]gktReply{
		"gk-oa": {200, "application/json", `{"input_tokens":999}`},
	})
	if err := provider.Save(provider.Provider{ID: "gkt", Name: "gkt",
		Chat: up.URL + "/v1", Anthropic: up.URL,
		Key: "gk-oa", KeyProtocol: provider.Chat}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	code, body := gktPost(t, gw.Handler(), "/v1/messages/count_tokens", `{"model":"gkt/claude-sonnet-5-5","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, c := range u.calls() {
		if strings.HasPrefix(c, "/v1/messages/count_tokens") {
			t.Fatalf("an OpenAI-only key answered a count_tokens request: %v", u.calls())
		}
	}
	if strings.Contains(body, "999") {
		t.Fatalf("count came from the OpenAI-only key: %s", body)
	}
}
