package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// mineHit is one request an mineUpstream saw.
type mineHit struct{ path, key, model string }

// mineUpstream records requests and answers through mineReply.
type mineUpstream struct {
	mu    sync.Mutex
	hits  []mineHit
	mineReply func(h mineHit, w http.ResponseWriter)
}

func (u *mineUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if k := r.Header.Get("x-api-key"); k != "" {
		key = k
	}
	h := mineHit{r.URL.Path, key, modelOf(b)}
	u.mu.Lock()
	u.hits = append(u.hits, h)
	u.mu.Unlock()
	u.mineReply(h, w)
}

func (u *mineUpstream) list() []mineHit {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]mineHit(nil), u.hits...)
}

const mineOkChat = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func mineJsonReply(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	io.WriteString(w, body)
}

func mineOkStatus(code int, body string) func(h mineHit, w http.ResponseWriter) {
	return func(h mineHit, w http.ResponseWriter) { mineJsonReply(w, code, body) }
}

func mineIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func mineServeUp(t *testing.T, u *mineUpstream) string {
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	return srv.URL
}

func mineSend(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const mineChatBody = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`

func mineChatReq(model string) string { return strings.Replace(mineChatBody, "%s", model, 1) }

func TestKeysRelayInOrder_Mine(t *testing.T) {
	mineIsolate(t)
	u := &mineUpstream{mineReply: func(h mineHit, w http.ResponseWriter) {
		if h.key == "k1" {
			mineJsonReply(w, 429, `{"error":{"message":"slow down"}}`)
			return
		}
		mineJsonReply(w, 200, mineOkChat)
	}}
	url := mineServeUp(t, u)
	err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Key: "k1", KeyName: "personal",
		Keys: []provider.Key{{Name: "team", Key: "k2"}, {Name: "spare", Key: "k3", Off: true}}, Models: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := mineSend(s, "/v1/chat/completions", mineChatReq("relay/m1"))
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "relay" || !strings.Contains(c.Fallback, "relay") || !strings.Contains(c.Fallback, "personal") {
		t.Errorf("call: %+v", c)
	}
	if code, _ = mineSend(s, "/v1/chat/completions", mineChatReq("relay/m1")); code != 200 {
		t.Fatal(code)
	}
	var keys []string
	for _, h := range u.list() {
		keys = append(keys, h.key)
	}
	if got := strings.Join(keys, ","); got != "k1,k2,k2" {
		t.Errorf("keys tried: %s (second request must go straight to k2, k3 is off)", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("second call should not have failed anything: %+v", c)
	}
}

func TestKeysBeforeFallbackModel_Mine(t *testing.T) {
	mineIsolate(t)
	u := &mineUpstream{mineReply: func(h mineHit, w http.ResponseWriter) {
		if h.key == "k1" {
			mineJsonReply(w, 402, `{"error":{"message":"insufficient balance"}}`)
			return
		}
		mineJsonReply(w, 200, mineOkChat)
	}}
	url := mineServeUp(t, u)
	f := &mineUpstream{mineReply: mineOkStatus(200, mineOkChat)}
	furl := mineServeUp(t, f)
	provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Key: "k1", Keys: []provider.Key{{Key: "k2"}},
		Models: []string{"m1"}, Fallback: []string{"other/fb-model"}})
	provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: furl + "/v1", Key: "o", Models: []string{"fb-model"}})
	s := New()
	if code, body := mineSend(s, "/v1/chat/completions", mineChatReq("relay/m1")); code != 200 {
		t.Fatal(code, body)
	}
	if len(f.list()) != 0 {
		t.Error("fallback model used before the second key")
	}
	if c := s.Recent()[0]; c.Provider != "relay" {
		t.Errorf("provider %s", c.Provider)
	}
}

func TestProtocolLimitedKeys_Mine(t *testing.T) {
	for _, order := range []string{"anthropic-first", "openai-first"} {
		t.Run(order, func(t *testing.T) {
			mineIsolate(t)
			u := &mineUpstream{mineReply: func(h mineHit, w http.ResponseWriter) {
				if h.path == "/v1/messages" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sse(
						`data: {"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}`,
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
						`data: {"type":"content_block_stop","index":0}`,
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
						`data: {"type":"message_stop"}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(`data: {"id":"c","choices":[{"delta":{"content":"hi"}}]}`,
					`data: {"id":"c","choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
			}}
			url := mineServeUp(t, u)
			p := provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Anthropic: url, Models: []string{"claude-x", "gpt-x"}}
			ak := provider.Key{Name: "a", Key: "ak", Protocol: provider.Anthropic}
			ck := provider.Key{Name: "c", Key: "ck", Protocol: provider.Chat}
			if order == "anthropic-first" {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = ak.Key, ak.Name, ak.Protocol, []provider.Key{ck}
			} else {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = ck.Key, ck.Name, ck.Protocol, []provider.Key{ak}
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			anth := `{"model":"%s","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
			// Claude Code, Claude model → Anthropic endpoint, Anthropic key
			if code, b := mineSend(s, "/v1/messages", strings.Replace(anth, "%s", "relay/claude-x", 1)); code != 200 {
				t.Fatal(code, b)
			}
			// OpenAI client, Claude model → translated to the Anthropic endpoint
			if code, b := mineSend(s, "/v1/chat/completions", mineChatReq("relay/claude-x")); code != 200 {
				t.Fatal(code, b)
			}
			// Claude Code, GPT model → translated to the chat endpoint with the OpenAI key
			if code, b := mineSend(s, "/v1/messages", strings.Replace(anth, "%s", "relay/gpt-x", 1)); code != 200 {
				t.Fatal(code, b)
			}
			want := []mineHit{{"/v1/messages", "ak", "claude-x"}, {"/v1/messages", "ak", "claude-x"}, {"/v1/chat/completions", "ck", "gpt-x"}}
			got := u.list()
			if len(got) != len(want) {
				t.Fatalf("hits %v", got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("hit %d = %v, want %v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestLimitedKeyNeverLeavesItsEndpoint_Mine(t *testing.T) {
	mineIsolate(t)
	u := &mineUpstream{mineReply: mineOkStatus(404, `{"error":{"message":"nope"}}`)}
	url := mineServeUp(t, u)
	provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Responses: url + "/v1", Key: "ck", KeyProtocol: provider.Chat, Models: []string{"m"}})
	s := New()
	mineSend(s, "/v1/responses", `{"model":"relay/m","input":"hi"}`)
	for _, h := range u.list() {
		if h.path != "/v1/chat/completions" {
			t.Errorf("chat-only key reached %s", h.path)
		}
	}
}

func TestCountTokensSkipsOpenAIOnlyKey_Mine(t *testing.T) {
	mineIsolate(t)
	u := &mineUpstream{mineReply: mineOkStatus(200, `{"input_tokens":42}`)}
	url := mineServeUp(t, u)
	provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Anthropic: url, Key: "ck", KeyProtocol: provider.Chat,
		Keys: []provider.Key{{Key: "ak", Protocol: provider.Anthropic}}, Models: []string{"claude-x"}})
	code, body := mineSend(New(), "/v1/messages/count_tokens", `{"model":"relay/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "42") {
		t.Fatal(code, body)
	}
	for _, h := range u.list() {
		if h.key != "ak" {
			t.Errorf("count_tokens used %q", h.key)
		}
	}
}

func mineFallbackSetup(t *testing.T, primary, fb http.HandlerFunc) (*Server, *mineUpstream, *mineUpstream) {
	mineIsolate(t)
	pu := &mineUpstream{mineReply: func(h mineHit, w http.ResponseWriter) { primary(w, nil) }}
	fu := &mineUpstream{mineReply: func(h mineHit, w http.ResponseWriter) { fb(w, nil) }}
	purl, furl := mineServeUp(t, pu), mineServeUp(t, fu)
	provider.Save(provider.Provider{ID: "main", Name: "Main", Chat: purl + "/v1", Key: "m", Models: []string{"big"},
		Fallback: []string{" other/small ", "other/small", "nonsense", "ghost/x", "/", "other/"}})
	provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: furl + "/v1", Key: "o", Models: []string{"small"}})
	return New(), pu, fu
}

func mineReply(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { mineJsonReply(w, code, body) }
}

func TestFallbackModel_Mine(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`},
		{"402", 402, `{"error":{"message":"pay up"}}`},
		{"529", 529, `{"error":{"message":"overloaded"}}`},
		{"503", 503, `{"error":{"message":"down"}}`},
		{"403 quota", 403, `{"error":{"message":"额度已用完"}}`},
		{"403 english", 403, `{"error":{"message":"You exceeded your current quota"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, pu, fu := mineFallbackSetup(t, mineReply(c.code, c.body), mineReply(200, mineOkChat))
			code, body := mineSend(s, "/v1/chat/completions", mineChatReq("main/big"))
			if code != 200 || !strings.Contains(body, `"ok"`) {
				t.Fatalf("%d %s", code, body)
			}
			if h := fu.list(); len(h) != 1 || h[0].model != "small" {
				t.Errorf("fallback mineUpstream saw %v", h)
			}
			if h := pu.list(); len(h) != 1 || h[0].model != "big" {
				t.Errorf("primary saw %v", h)
			}
			call := s.Recent()[0]
			if call.Provider != "other" || !strings.Contains(call.Fallback, "main") {
				t.Errorf("call %+v", call)
			}
			// the failed provider rests: next request goes straight to the fallback
			if code, _ := mineSend(s, "/v1/chat/completions", mineChatReq("main/big")); code != 200 {
				t.Fatal(code)
			}
			if len(pu.list()) != 1 || len(fu.list()) != 2 {
				t.Errorf("primary %d, fallback %d hits", len(pu.list()), len(fu.list()))
			}
		})
	}
}

func TestFallbackRestEnds_Mine(t *testing.T) {
	old := relayCooldown
	relayCooldown = 30 * time.Millisecond
	defer func() { relayCooldown = old }()
	s, pu, _ := mineFallbackSetup(t, mineReply(429, `{"error":{"message":"x"}}`), mineReply(200, mineOkChat))
	mineSend(s, "/v1/chat/completions", mineChatReq("main/big"))
	time.Sleep(60 * time.Millisecond)
	mineSend(s, "/v1/chat/completions", mineChatReq("main/big"))
	if len(pu.list()) != 2 {
		t.Errorf("primary should be tried again after resting, hits %d", len(pu.list()))
	}
}

func TestBadRequestDoesNotFallBack_Mine(t *testing.T) {
	s, _, fu := mineFallbackSetup(t, mineReply(400, `{"error":{"message":"bad param"}}`), mineReply(200, mineOkChat))
	code, body := mineSend(s, "/v1/chat/completions", mineChatReq("main/big"))
	if code != 400 || !strings.Contains(body, "bad param") {
		t.Fatalf("%d %s", code, body)
	}
	if len(fu.list()) != 0 {
		t.Error("fell back on 400")
	}
	if s.Recent()[0].Fallback != "" {
		t.Error("fallback recorded")
	}
	// a 403 that is not about quota is not retried either
	s, _, fu = mineFallbackSetup(t, mineReply(403, `{"error":{"message":"forbidden region"}}`), mineReply(200, mineOkChat))
	if code, _ := mineSend(s, "/v1/chat/completions", mineChatReq("main/big")); code != 403 || len(fu.list()) != 0 {
		t.Errorf("403: %d, fallback hits %d", code, len(fu.list()))
	}
}

func TestAllCandidatesFailReturnsLastError_Mine(t *testing.T) {
	s, pu, fu := mineFallbackSetup(t, mineReply(429, `{"error":{"message":"first"}}`), mineReply(503, `{"error":{"message":"last one"}}`))
	code, body := mineSend(s, "/v1/chat/completions", mineChatReq("main/big"))
	if code != 503 || !strings.Contains(body, "last one") || strings.Contains(body, "first") {
		t.Fatalf("%d %s", code, body)
	}
	if len(pu.list()) != 1 || len(fu.list()) != 1 {
		t.Error("each candidate once")
	}
	if c := s.Recent()[0]; c.Status != 503 || c.Fallback == "" {
		t.Errorf("call %+v", c)
	}
}

func TestFallbackFromStreamingStart_Mine(t *testing.T) {
	s, _, fu := mineFallbackSetup(t, mineReply(429, `{"error":{"message":"x"}}`), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","choices":[{"delta":{"content":"streamed"}}]}`, `data: [DONE]`))
	})
	code, body := mineSend(s, "/v1/chat/completions", `{"model":"main/big","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "streamed") || len(fu.list()) != 1 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestRetryable_Mine(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{429, "", true}, {402, "", true}, {529, "", true}, {503, "", true}, {400, "quota exceeded", false},
		{403, "余额不足", true}, {403, "forbidden", false}, {401, "", false}, {500, "plain", false}, {500, "overloaded", true},
	} {
		if got := relayRetryable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("retryable(%d, %q) = %v", c.status, c.body, got)
		}
	}
}
