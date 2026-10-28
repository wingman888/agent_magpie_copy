package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func do(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	gateway.New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

type upstream struct {
	mu   sync.Mutex
	seen []string
}

func (u *upstream) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.seen
	u.seen = nil
	return s
}

func (u *upstream) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		u.mu.Lock()
		u.seen = append(u.seen, r.URL.Path+" "+key)
		u.mu.Unlock()
		if (r.URL.Path == "/v1/messages") != (key == "k-ant") {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"this key is for another protocol"}}`)
			return
		}
		stream := strings.Contains(string(b), `"stream":true`)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			if r.URL.Path == "/v1/messages" {
				io.WriteString(w, strings.Join([]string{
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
					"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n",
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n",
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"}, ""))
				return
			}
			io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" {
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func relayCases(t *testing.T, p provider.Provider) {
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptKeysMatchProtocolAnyOrder(t *testing.T) {
	for _, oaiFirst := range []bool{true, false} {
		isolate(t)
		u := &upstream{}
		srv := u.start(t)
		p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
			Models: []string{"claude-opus-4-8", "gpt-5.5"}}
		oai := provider.KeyAccount{Key: "k-oai", Protocol: provider.Chat}
		ant := provider.KeyAccount{Key: "k-ant", Protocol: provider.Anthropic}
		if oaiFirst {
			p.Key, p.KeyProtocol, p.Keys = oai.Key, oai.Protocol, []provider.KeyAccount{ant}
		} else {
			p.Key, p.KeyProtocol, p.Keys = ant.Key, ant.Protocol, []provider.KeyAccount{oai}
		}
		relayCases(t, p)
		for _, x := range []struct{ path, body, want string }{
			{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
			{"/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
			{"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
			{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
			{"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
		} {
			u.take()
			code, body := do(t, x.path, x.body)
			seen := u.take()
			if code != 200 || len(seen) != 1 || seen[0] != x.want {
				t.Fatalf("oaiFirst=%v %s %s: %d %s, upstream saw %v", oaiFirst, x.path, x.body, code, body, seen)
			}
		}
	}
}

func TestAcceptOpenAIOnlyProviderWithAnthropicKey(t *testing.T) {
	isolate(t)
	u := &upstream{}
	srv := u.start(t)
	relayCases(t, provider.Provider{ID: "solo", Name: "Solo", Chat: srv.URL + "/v1",
		Key: "k-ant", KeyProtocol: provider.Anthropic, Models: []string{"gpt-5.5"}})
	// must not crash; the lone key falls back to the single candidate
	code, body := do(t, "/v1/chat/completions", `{"model":"solo/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code == 0 || code >= 600 {
		t.Fatalf("%d %s", code, body)
	}
	if seen := u.take(); len(seen) == 0 {
		t.Fatal("request was not forwarded to the only endpoint")
	}
}

func TestAcceptUnconstrainedKeysUnchanged(t *testing.T) {
	isolate(t)
	u := &upstream{}
	srv := u.start(t)
	relayCases(t, provider.Provider{ID: "plain", Name: "Plain", Chat: srv.URL + "/v1",
		Key: "k-ant", Models: []string{"deepseek-chat"}})
	code, body := do(t, "/v1/chat/completions", `{"model":"plain/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	// upstream rejects non-/v1/messages with k-ant (401); we only check the key and path used
	seen := u.take()
	if len(seen) == 0 || seen[0] != "/v1/chat/completions k-ant" {
		t.Fatalf("%d %s %v", code, body, seen)
	}
}

func TestAcceptCountTokensSkipsOpenAIOnlyKeys(t *testing.T) {
	isolate(t)
	u := &upstream{}
	srv := u.start(t)
	relayCases(t, provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "k-oai", KeyProtocol: provider.Chat, Models: []string{"claude-opus-4-8"}})
	code, body := do(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)
	for _, s := range u.take() {
		if strings.HasPrefix(s, "/v1/messages/count_tokens") {
			t.Fatalf("count_tokens forwarded upstream: %s", s)
		}
	}
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", code, body)
	}
}
