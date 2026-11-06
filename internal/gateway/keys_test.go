package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kpFake is an upstream that answers by the key it was sent: a key maps
// to a status, and anything without a status answers 200 with a reply in the
// shape of the endpoint it was called on.
type kpFake struct {
	status map[string]int
	seen   []string // keys, in the order they were tried
	paths  []string
}

const kpAnthropicReply = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

data: {"type":"message_stop"}

`

const kpChatReply = `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}

data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}

data: [DONE]

`

func (f *kpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	f.seen = append(f.seen, key)
	f.paths = append(f.paths, r.URL.Path)
	if code := f.status[key]; code != 0 && code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if strings.HasSuffix(r.URL.Path, "/messages") {
		io.WriteString(w, kpAnthropicReply)
		return
	}
	io.WriteString(w, kpChatReply)
}

// kpSetup saves provider "relay" with the given key in use and endpoints,
// and returns a gateway that serves it.
func kpSetup(t *testing.T, f *kpFake, top, anthropic, chat string) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: top}
	if anthropic != "" {
		p.Anthropic = up.URL
	}
	if chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New()
}

func kpPostTo(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const kpClaudeBody = `{"model":"relay/claude-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

func TestSecondKeyFinishesWhenFirstIsRateLimited(t *testing.T) {
	f := &kpFake{status: map[string]int{"k1": 429}}
	srv := kpSetup(t, f, "k1", "anthropic", "")
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("relay", provider.KeyRef("k1"), "personal"); err != nil {
		t.Fatal(err)
	}

	rec := kpPostTo(t, srv, "/v1/messages", kpClaudeBody)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Join(f.seen, ",") != "k1,k2" {
		t.Fatalf("keys tried = %v", f.seen)
	}
	calls := srv.Recent()
	if calls[0].Provider != "relay" || !strings.Contains(calls[0].Fallback, "relay/personal") || !strings.Contains(calls[0].Fallback, "429") {
		t.Fatalf("call record = %+v", calls[0])
	}

	// the next request goes straight to the second key; the first rests
	kpPostTo(t, srv, "/v1/messages", kpClaudeBody)
	if strings.Join(f.seen, ",") != "k1,k2,k2" {
		t.Fatalf("keys tried on the next request = %v", f.seen)
	}
	if calls := srv.Recent(); calls[0].Fallback != "" {
		t.Errorf("second request recorded a fallback: %q", calls[0].Fallback)
	}
}

func TestDisabledKeyIsNeverTried(t *testing.T) {
	f := &kpFake{status: map[string]int{"k1": 429}}
	srv := kpSetup(t, f, "k1", "anthropic", "")
	if err := provider.AddKey("relay", "off", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "on", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", provider.KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	if rec := kpPostTo(t, srv, "/v1/messages", kpClaudeBody); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Join(f.seen, ",") != "k1,k3" {
		t.Fatalf("keys tried = %v; the disabled key must not appear", f.seen)
	}
}

func TestLastKeyErrorIsReturned(t *testing.T) {
	f := &kpFake{status: map[string]int{"k1": 429}}
	srv := kpSetup(t, f, "k1", "anthropic", "")
	rec := kpPostTo(t, srv, "/v1/messages", kpClaudeBody)
	if rec.Code != 429 {
		t.Fatalf("status %d, want the upstream's 429", rec.Code)
	}
	if calls := srv.Recent(); calls[0].Fallback != "" {
		t.Errorf("single key recorded a fallback: %q", calls[0].Fallback)
	}
}

func TestBadRequestIsNotRetriedWithAnotherKey(t *testing.T) {
	f := &kpFake{status: map[string]int{"k1": 400}}
	srv := kpSetup(t, f, "k1", "anthropic", "")
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if rec := kpPostTo(t, srv, "/v1/messages", kpClaudeBody); rec.Code != 400 {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if strings.Join(f.seen, ",") != "k1" {
		t.Fatalf("keys tried = %v; a 400 must not move to another key", f.seen)
	}
}

func TestProtocolLimitedKeysReachOnlyTheirEndpoint(t *testing.T) {
	f := &kpFake{}
	srv := kpSetup(t, f, "oai", "anthropic", "chat")
	if err := provider.SetKeyProtocol("relay", provider.KeyRef("oai"), provider.Chat); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "ant", "ant", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	try := func(name, path, body, wantKey, wantPath string) {
		t.Helper()
		f.seen, f.paths = nil, nil
		kpPostTo(t, srv, path, body)
		if len(f.seen) == 0 || f.seen[0] != wantKey || f.paths[0] != wantPath {
			t.Errorf("%s: first try key=%v path=%v, want %s %s", name, f.seen, f.paths, wantKey, wantPath)
		}
	}
	// Claude Code asks for a Claude model: the Anthropic key, Anthropic endpoint
	try("claude from anthropic client", "/v1/messages", kpClaudeBody, "ant", "/v1/messages")
	// an OpenAI client asks for a Claude model: translated onto the Anthropic endpoint
	try("claude from chat client", "/v1/chat/completions",
		`{"model":"relay/claude-x","messages":[{"role":"user","content":"hi"}]}`, "ant", "/v1/messages")
	// Claude Code asks for a GPT model: translated onto the OpenAI-style endpoint
	try("gpt from anthropic client", "/v1/messages",
		`{"model":"relay/gpt-5.5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "oai", "/v1/chat/completions")
}

func TestCountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	f := &kpFake{}
	srv := kpSetup(t, f, "oai", "anthropic", "chat")
	if err := provider.SetKeyProtocol("relay", provider.KeyRef("oai"), provider.Chat); err != nil {
		t.Fatal(err)
	}
	rec := kpPostTo(t, srv, "/v1/messages/count_tokens", kpClaudeBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.seen) != 0 {
		t.Fatalf("count_tokens reached upstream with an OpenAI-only key: %v %v", f.seen, f.paths)
	}
}

func TestCallRecordsTheKeyThatAnswered(t *testing.T) {
	f := &kpFake{status: map[string]int{"k1": 429}}
	srv := kpSetup(t, f, "k1", "anthropic", "")
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	kpPostTo(t, srv, "/v1/messages", kpClaudeBody)
	if calls := srv.Recent(); calls[0].Key != "team" {
		t.Fatalf("call key = %q, want the key that answered (team)", calls[0].Key)
	}
	// k1 is resting after its 429, so the next request is answered by team
	kpPostTo(t, srv, "/v1/messages", kpClaudeBody)
	if calls := srv.Recent(); calls[0].Key != "team" {
		t.Fatalf("second call key = %q, want team", calls[0].Key)
	}
}
