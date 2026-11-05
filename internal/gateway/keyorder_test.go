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

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]family{
		"claude-opus-4-8":                   familyAnthropic,
		"relay/claude-opus-4-8":             familyAnthropic,
		"gpt-5.5":                           familyOpenAI,
		"relay/gpt-5.5":                     familyOpenAI,
		"openrouter/openai/o3-mini":         familyOpenAI,
		"o1-preview":                        familyOpenAI,
		"my-codex-model":                    familyOpenAI,
		"deepseek-chat":                     familyUnknown,
		"openrouter/deepseek/deepseek-chat": familyUnknown,
		"":                                  familyUnknown,
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %v, want %v", model, got, want)
		}
	}
}

// relayProvider is one relay with a Chat and an Anthropic endpoint, and two
// keys, each limited to one of them. chatFirst says which one comes first.
func relayProvider(base string, chatFirst bool) provider.Provider {
	chatKey := provider.KeyAccount{Name: "Chat", Key: "k-chat", Protocol: provider.Chat}
	antKey := provider.KeyAccount{Name: "Anthropic", Key: "k-anth", Protocol: provider.Anthropic}
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Anthropic: base,
		Models: []string{"claude-opus-4-8", "gpt-5.5"}}
	if chatFirst {
		p.Key, p.KeyName, p.KeyProtocol, p.Keys = chatKey.Key, chatKey.Name, chatKey.Protocol, []provider.KeyAccount{antKey}
	} else {
		p.Key, p.KeyName, p.KeyProtocol, p.Keys = antKey.Key, antKey.Name, antKey.Protocol, []provider.KeyAccount{chatKey}
	}
	return p
}

func keyNames(ks []provider.KeyAccount) string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Key)
	}
	return strings.Join(out, ",")
}

func TestKeyOrderFollowsModelFamily(t *testing.T) {
	cases := []struct {
		model string
		from  provider.Protocol
		want  string
	}{
		{"claude-opus-4-8", provider.Anthropic, "k-anth,k-chat"},
		{"claude-opus-4-8", provider.Chat, "k-anth,k-chat"},
		{"gpt-5.5", provider.Anthropic, "k-chat,k-anth"},
		{"gpt-5.5", provider.Chat, "k-chat,k-anth"},
		// unknown family: the key that speaks the client's protocol first
		{"deepseek-chat", provider.Anthropic, "k-anth,k-chat"},
		{"deepseek-chat", provider.Chat, "k-chat,k-anth"},
	}
	for _, chatFirst := range []bool{true, false} {
		for _, c := range cases {
			p := relayProvider("http://relay.invalid", chatFirst)
			ks, ok := usableKeys(p, c.model, c.from, p.KeysOn())
			if !ok || keyNames(ks) != c.want {
				t.Errorf("chatFirst=%v model=%s from=%s: got %q (ok %v), want %q",
					chatFirst, c.model, c.from, keyNames(ks), ok, c.want)
			}
		}
	}
}

func TestUnlimitedKeysKeepTheirOrder(t *testing.T) {
	p := provider.Provider{ID: "plain", Name: "Plain", Chat: "http://plain.invalid/v1", Anthropic: "http://plain.invalid",
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	for _, model := range []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat"} {
		for _, from := range provider.Protocols {
			ks, ok := usableKeys(p, model, from, p.KeysOn())
			if !ok || keyNames(ks) != "k1,k2,k3" {
				t.Errorf("model %s from %s: got %q", model, from, keyNames(ks))
			}
		}
	}
}

func TestCandidatesUseTheKeysEndpoints(t *testing.T) {
	s := New()
	for _, chatFirst := range []bool{true, false} {
		p := relayProvider("http://relay.invalid", chatFirst)
		cs := s.candidatesFrom(p, "claude-opus-4-8", provider.Chat)
		if len(cs) != 2 || cs[0].p.Key != "k-anth" || cs[0].p.Chat != "" || cs[0].p.Anthropic == "" {
			t.Fatalf("chatFirst=%v: claude candidates %+v", chatFirst, cs)
		}
		if cs[1].p.Key != "k-chat" || cs[1].p.Anthropic != "" || cs[1].p.Chat == "" {
			t.Fatalf("chatFirst=%v: second candidate %+v", chatFirst, cs[1])
		}
		cs = s.candidatesFrom(p, "gpt-5.5", provider.Anthropic)
		if len(cs) != 2 || cs[0].p.Key != "k-chat" || cs[0].p.Anthropic != "" {
			t.Fatalf("chatFirst=%v: gpt candidates %+v", chatFirst, cs)
		}
	}
}

func TestKeysWithoutTheirEndpointAreIgnored(t *testing.T) {
	s := New()
	// only a Chat endpoint: the Anthropic-limited key does not take part
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat.invalid/v1",
		Key: "k-anth", KeyProtocol: provider.Anthropic, Keys: []provider.KeyAccount{{Key: "k-gen"}}}
	ks, ok := usableKeys(p, "claude-opus-4-8", provider.Anthropic, p.KeysOn())
	if !ok || keyNames(ks) != "k-gen" {
		t.Fatalf("usable keys %q (ok %v), want k-gen", keyNames(ks), ok)
	}
	cs := s.candidatesFrom(p, "claude-opus-4-8", provider.Anthropic)
	if len(cs) != 1 || cs[0].p.Key != "k-gen" {
		t.Fatalf("candidates %+v, want only k-gen", cs)
	}
	// every key ignored: the default, the keys as they are
	q := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat.invalid/v1",
		Key: "k-anth", KeyProtocol: provider.Anthropic}
	if _, ok := usableKeys(q, "claude-opus-4-8", provider.Anthropic, q.KeysOn()); ok {
		t.Fatal("a key without its endpoint was usable")
	}
	cs = s.candidatesFrom(q, "claude-opus-4-8", provider.Anthropic)
	if len(cs) != 1 || cs[0].p.Key != "k-anth" {
		t.Fatalf("default candidates %+v, want k-anth", cs)
	}
}

// relayUpstream serves both endpoints of one relay and records each request
// as the path and the credentials it carried.
type relayUpstream struct {
	mu   sync.Mutex
	seen []string
}

func (u *relayUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" x-api-key="+r.Header.Get("x-api-key")+" bearer="+r.Header.Get("Authorization"))
	u.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "count_tokens"):
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"gpt-5.5","choices":[{"delta":{"role":"assistant","content":"pong"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"message_start","message":{"id":"m1","model":"claude-opus-4-8","usage":{"input_tokens":3,"output_tokens":0}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`))
	}
}

func (u *relayUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// saveRelay points magpie at a relay served by up, keys in the given order.
func saveRelay(t *testing.T, up *httptest.Server, chatFirst bool) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	if err := provider.Save(relayProvider(up.URL, chatFirst)); err != nil {
		t.Fatal(err)
	}
}

func TestKeyFollowsTheModelEndToEnd(t *testing.T) {
	cases := []struct {
		name, path, body string
		wantCall         string // path and credential of the one upstream call
		wantBody         string
	}{
		{"anthropic client, claude model",
			"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"/v1/messages x-api-key=k-anth", "pong"},
		{"anthropic client, claude model, streamed",
			"/v1/messages", `{"model":"relay/claude-opus-4-8","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"/v1/messages x-api-key=k-anth", "pong"},
		{"chat client, claude model",
			"/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`,
			"/v1/messages x-api-key=k-anth", "pong"},
		{"chat client, claude model, streamed",
			"/v1/chat/completions", `{"model":"relay/claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"/v1/messages x-api-key=k-anth", "pong"},
		{"anthropic client, gpt model",
			"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"/v1/chat/completions x-api-key= bearer=Bearer k-chat", "pong"},
		{"anthropic client, gpt model, streamed",
			"/v1/messages", `{"model":"relay/gpt-5.5","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
			"/v1/chat/completions x-api-key= bearer=Bearer k-chat", "pong"},
	}
	for _, chatFirst := range []bool{true, false} {
		for _, c := range cases {
			t.Run(c.name+" chatFirst="+map[bool]string{true: "yes", false: "no"}[chatFirst], func(t *testing.T) {
				up := &relayUpstream{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				saveRelay(t, srv, chatFirst)
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), c.wantBody) {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				calls := up.calls()
				if len(calls) != 1 || !strings.Contains(calls[0], c.wantCall) {
					t.Fatalf("upstream got %v, want one call %q", calls, c.wantCall)
				}
			})
		}
	}
}

func TestCountTokensNeverSentToAnthropicWithoutAnthropicKey(t *testing.T) {
	up := &relayUpstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	// only an OpenAI-limited key: the Anthropic endpoint must not be called
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "k-chat", KeyProtocol: provider.Chat, Models: []string{"claude-opus-4-8"}}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if calls := up.calls(); len(calls) != 0 {
		t.Fatalf("upstream got %v, want no calls", calls)
	}
}

func TestCountTokensUsesTheAnthropicKey(t *testing.T) {
	up := &relayUpstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveRelay(t, srv, true)
	body := `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"input_tokens":7`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	calls := up.calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "/v1/messages/count_tokens x-api-key=k-anth") {
		t.Fatalf("upstream got %v", calls)
	}
}
