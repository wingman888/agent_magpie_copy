package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// endpointLog records which endpoint and key each upstream request used, as
// "anthropic:<key>" or "chat:<key>". fail makes one of them answer a status.
type endpointLog struct {
	mu   sync.Mutex
	seen []string
	fail map[string]int
}

func (l *endpointLog) add(who string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, who)
	return l.fail[who]
}

// took returns the requests seen since the last call, in order, and forgets them.
func (l *endpointLog) took() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := strings.Join(l.seen, ",")
	l.seen = nil
	return out
}

var (
	anthropicJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	anthropicSSE  = sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`)
	chatSSE = sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`)
)

// upstreams starts an Anthropic-style and an OpenAI Chat-style endpoint that
// log into l. Each answers streamed and plain requests as its protocol does.
func upstreams(t *testing.T, l *endpointLog) (anth, chat *httptest.Server) {
	t.Helper()
	anth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if code := l.add("anthropic:" + r.Header.Get("x-api-key")); code != 0 {
			w.WriteHeader(code)
			io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, anthropicSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicJSON)
	}))
	chat = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if code := l.add("chat:" + key); code != 0 {
			w.WriteHeader(code)
			io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, chatSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChat)
	}))
	t.Cleanup(anth.Close)
	t.Cleanup(chat.Close)
	return anth, chat
}

// twoKeys saves a relay with two keys: ak, limited to Anthropic, and ck, limited
// to Chat. swapped saves ck first, so the file lists the keys in the other order.
func twoKeys(t *testing.T, anth, chat *httptest.Server, swapped bool) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	type keyed struct {
		name, key string
		proto     provider.Protocol
	}
	first, second := keyed{"anthropic", "ak", provider.Anthropic}, keyed{"openai", "ck", provider.Chat}
	if swapped {
		first, second = second, first
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: anth.URL, Chat: chat.URL + "/v1",
		Key: first.key, KeyName: first.name, KeyProtocol: first.proto}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", second.name, second.key, second.proto); err != nil {
		t.Fatal(err)
	}
}

// send posts one request to h and returns the status.
func send(h http.Handler, path, body string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code
}

// A key limited to one protocol reaches only that protocol's endpoint, and the
// choice of key does not depend on the order the keys were saved in.
func TestProtocolLimitedKeysRouting(t *testing.T) {
	cases := []struct {
		name, path, body string
		want             string // upstream requests, in order
	}{
		// Claude Code asking for a Claude model: the Anthropic key, relayed
		{"claude code, claude model", "/v1/messages",
			`{"model":"relay/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "anthropic:ak"},
		// an OpenAI client asking for a Claude model: translated to the Anthropic endpoint
		{"openai client, claude model", "/v1/chat/completions",
			`{"model":"relay/claude-x","messages":[{"role":"user","content":"hi"}]}`, "anthropic:ak"},
		// Claude Code asking for a GPT model: translated to the OpenAI endpoint
		{"claude code, gpt model", "/v1/messages",
			`{"model":"relay/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "chat:ck"},
		// a model of neither family prefers the key that needs no translation
		{"claude code, other model", "/v1/messages",
			`{"model":"relay/deepseek-chat","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "anthropic:ak"},
	}
	for _, swapped := range []bool{false, true} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s swapped=%v", c.name, swapped), func(t *testing.T) {
				l := &endpointLog{}
				anth, chat := upstreams(t, l)
				twoKeys(t, anth, chat, swapped)
				if code := send(New().Handler(), c.path, c.body); code != 200 {
					t.Fatalf("status %d", code)
				}
				if got := l.took(); got != c.want {
					t.Fatalf("upstream saw %s, want %s", got, c.want)
				}
			})
		}
	}
}

// A rate-limited Anthropic key hands a Claude request to the OpenAI key, which
// is then translated; the next request goes straight to that key.
func TestLimitedKeyFallsBackAcrossProtocols(t *testing.T) {
	for _, swapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("swapped=%v", swapped), func(t *testing.T) {
			l := &endpointLog{fail: map[string]int{"anthropic:ak": 429}}
			anth, chat := upstreams(t, l)
			twoKeys(t, anth, chat, swapped)
			h := New().Handler()
			body := `{"model":"relay/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
			if code := send(h, "/v1/messages", body); code != 200 {
				t.Fatalf("status %d, want the OpenAI key's 200", code)
			}
			if got := l.took(); got != "anthropic:ak,chat:ck" {
				t.Fatalf("first request sent %s", got)
			}
			if code := send(h, "/v1/messages", body); code != 200 {
				t.Fatalf("second status %d", code)
			}
			if got := l.took(); got != "chat:ck" {
				t.Fatalf("second request sent %s, want chat:ck only", got)
			}
		})
	}
}

// Keys that fit no endpoint of the provider are never sent.
func TestKeyWithoutMatchingEndpointIsNotUsed(t *testing.T) {
	l := &endpointLog{}
	anth, chat := upstreams(t, l)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// the relay has no Responses endpoint, so a Responses-limited key has
	// nowhere to go; the Chat key alone serves the request
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: anth.URL, Chat: chat.URL + "/v1",
		Key: "ck", KeyProtocol: provider.Chat}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "resp", "rk", provider.Responses); err != nil {
		t.Fatal(err)
	}
	if code := send(New().Handler(), "/v1/messages",
		`{"model":"relay/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := l.took(); got != "chat:ck" {
		t.Fatalf("upstream saw %s, want chat:ck", got)
	}
}

// Anthropic token counts use only keys that have an Anthropic endpoint: a
// key limited to OpenAI is never sent to the Anthropic endpoint.
func TestCountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	body := `{"model":"relay/claude-x","messages":[{"role":"user","content":"hi"}]}`
	t.Run("only an openai key", func(t *testing.T) {
		l := &endpointLog{}
		anth, chat := upstreams(t, l)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Anthropic: anth.URL, Chat: chat.URL + "/v1",
			Key: "ck", KeyProtocol: provider.Chat}); err != nil {
			t.Fatal(err)
		}
		if code := send(New().Handler(), "/v1/messages/count_tokens", body); code != 200 {
			t.Fatalf("status %d", code)
		}
		if got := l.took(); got != "" {
			t.Fatalf("count_tokens reached %s; want no upstream request", got)
		}
	})
	for _, swapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("with an anthropic key swapped=%v", swapped), func(t *testing.T) {
			l := &endpointLog{}
			anth, chat := upstreams(t, l)
			twoKeys(t, anth, chat, swapped)
			if code := send(New().Handler(), "/v1/messages/count_tokens", body); code != 200 {
				t.Fatalf("status %d", code)
			}
			if got := l.took(); got != "anthropic:ak" {
				t.Fatalf("count_tokens sent %s, want anthropic:ak", got)
			}
		})
	}
}
