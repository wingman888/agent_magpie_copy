package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const apAnthropicReply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}`

// apHit is one request an upstream received: its path and the key it was sent with.
type apHit struct{ path, key string }

// apUpstream records every request and answers with a fixed reply.
type apUpstream struct {
	mu    sync.Mutex
	hits  []apHit
	reply string
}

func (u *apUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.hits = append(u.hits, apHit{path: r.URL.Path, key: key})
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, u.reply)
}

func (u *apUpstream) got() []apHit {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]apHit(nil), u.hits...)
}

// postTo sends a body to a path on the gateway.
func postTo(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// relayKeys saves a provider with one Anthropic-only key and one OpenAI-only
// key, in the given order, and returns the two upstreams.
func relayKeys(t *testing.T, anthropicFirst bool) (*apUpstream, *apUpstream) {
	t.Helper()
	isolate(t)
	ant := &apUpstream{reply: apAnthropicReply}
	oai := &apUpstream{reply: fbChatOK}
	antSrv := httptest.NewServer(ant)
	oaiSrv := httptest.NewServer(oai)
	t.Cleanup(antSrv.Close)
	t.Cleanup(oaiSrv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-sonnet-4", "gpt-5.5"},
		Anthropic: antSrv.URL, Chat: oaiSrv.URL + "/v1"}
	if anthropicFirst {
		p.Key, p.KeyProtocol = "sk-ant", provider.Anthropic
		p.Keys = []provider.ExtraKey{{Key: "sk-oai", Protocol: provider.Chat}}
	} else {
		p.Key, p.KeyProtocol = "sk-oai", provider.Chat
		p.Keys = []provider.ExtraKey{{Key: "sk-ant", Protocol: provider.Anthropic}}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return ant, oai
}

const apAnthropicBody = `{"model":"relay/%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
const apChatBody = `{"model":"relay/%s","messages":[{"role":"user","content":"hi"}]}`

func TestProtocolKeysRouteClaudeModelsToAnthropic(t *testing.T) {
	for _, first := range []bool{true, false} {
		ant, oai := relayKeys(t, first)
		// Claude Code (Anthropic client) asks for a Claude model
		if code, body := postTo(New(), "/v1/messages", strings.Replace(apAnthropicBody, "%s", "claude-sonnet-4", 1)); code != 200 {
			t.Fatalf("anthropicFirst=%v: claude via anthropic client: %d %s", first, code, body)
		}
		if want := []apHit{{"/v1/messages", "sk-ant"}}; !reflect.DeepEqual(ant.got(), want) {
			t.Fatalf("anthropicFirst=%v: anthropic upstream got %v, want %v", first, ant.got(), want)
		}
		if len(oai.got()) != 0 {
			t.Fatalf("anthropicFirst=%v: openai upstream was asked: %v", first, oai.got())
		}
	}
}

func TestProtocolKeysTranslateClaudeModelsForOpenAIClients(t *testing.T) {
	for _, first := range []bool{true, false} {
		ant, oai := relayKeys(t, first)
		if code, body := postTo(New(), "/v1/chat/completions", strings.Replace(apChatBody, "%s", "claude-sonnet-4", 1)); code != 200 {
			t.Fatalf("anthropicFirst=%v: claude via chat client: %d %s", first, code, body)
		}
		if want := []apHit{{"/v1/messages", "sk-ant"}}; !reflect.DeepEqual(ant.got(), want) {
			t.Fatalf("anthropicFirst=%v: anthropic upstream got %v, want %v", first, ant.got(), want)
		}
		if len(oai.got()) != 0 {
			t.Fatalf("anthropicFirst=%v: openai upstream was asked: %v", first, oai.got())
		}
	}
}

func TestProtocolKeysTranslateGPTModelsForClaudeCode(t *testing.T) {
	for _, first := range []bool{true, false} {
		ant, oai := relayKeys(t, first)
		if code, body := postTo(New(), "/v1/messages", strings.Replace(apAnthropicBody, "%s", "gpt-5.5", 1)); code != 200 {
			t.Fatalf("anthropicFirst=%v: gpt via anthropic client: %d %s", first, code, body)
		}
		if want := []apHit{{"/v1/chat/completions", "sk-oai"}}; !reflect.DeepEqual(oai.got(), want) {
			t.Fatalf("anthropicFirst=%v: openai upstream got %v, want %v", first, oai.got(), want)
		}
		if len(ant.got()) != 0 {
			t.Fatalf("anthropicFirst=%v: anthropic upstream was asked: %v", first, ant.got())
		}
	}
}

func TestFallbackStatusesThatMoveOnAndThoseThatDoNot(t *testing.T) {
	moves := []fbReply{
		{code: 529, body: `{"error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{code: 402, body: `{"error":{"message":"payment required"}}`},
		{code: 403, body: `{"error":{"message":"额度已用完"}}`},
		{code: 403, body: `{"error":{"message":"quota exceeded for this key"}}`},
		{code: 429, body: `{"error":{"message":"slow down"}}`},
	}
	for _, rp := range moves {
		isolate(t)
		pri := &modelUpstream{replies: map[string]fbReply{"m1": rp}}
		bak := &modelUpstream{}
		saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
		saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)
		s := New()
		if code, body := postChat(s, fbRequest); code != 200 || !strings.Contains(body, "hi") {
			t.Fatalf("status %d (%s): client got %d %s; want the fallback's reply", rp.code, rp.body, code, body)
		}
		if want := []string{"m2"}; !reflect.DeepEqual(bak.asked(), want) {
			t.Fatalf("status %d: fallback asked for %v, want %v", rp.code, bak.asked(), want)
		}
	}

	stays := []fbReply{
		{code: 403, body: `{"error":{"message":"model not allowed for this key"}}`},
		{code: 422, body: `{"error":{"message":"bad field"}}`},
	}
	for _, rp := range stays {
		isolate(t)
		pri := &modelUpstream{replies: map[string]fbReply{"m1": rp}}
		bak := &modelUpstream{}
		saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
		saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)
		if code, body := postChat(New(), fbRequest); code != rp.code {
			t.Fatalf("status %d: client got %d %s; want it returned as it is", rp.code, code, body)
		}
		if len(bak.asked()) != 0 {
			t.Fatalf("status %d: fallback was asked: %v", rp.code, bak.asked())
		}
	}
}
