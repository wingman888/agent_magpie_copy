package gateway

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

const (
	chatRefusal      = "model m1 is not accessible via the /chat/completions endpoint"
	responsesRefusal = "model m1 is not accessible via the /responses endpoint"
	chatBody         = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	messagesBody     = `{"model":"m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
)

// When every API turns the model away, the first request tries each once and
// the last refusal reaches the agent. A later request asks only the preferred
// API (Chat, for a non-OpenAI model), once; the others are not asked.
func TestAllRefusedAsksPreferredOnce(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      refusal(chatRefusal),
		responsesPath: refusal(responsesRefusal),
	})
	code, body := ask(t, h, "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, responsesRefusal) {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
	code, body = ask(t, h, "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, chatRefusal) {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(chatPath) != 2 || f.count(responsesPath) != 1 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
}

// A refused candidate's reply is held back: when another API then answers,
// the agent sees only that answer, none of the refusal.
func TestRefusedReplyLeaksNothing(t *testing.T) {
	_, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      refusal(chatRefusal),
		responsesPath: responsesOK,
	})
	code, body := ask(t, h, "/v1/chat/completions", chatBody)
	if code != 200 || !strings.Contains(body, "from responses") {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, "not accessible") {
		t.Fatalf("refusal leaked to the agent: %s", body)
	}
}

// An Anthropic client on the passthrough path: /v1/messages turns the model
// away, and the request goes on to Chat, translated back to Messages.
func TestAnthropicClientPassthroughRefusedThenChat(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Anthropic: "x"}, map[string]scriptReply{
		messagesPath: refusal("model m1 is not accessible via the /v1/messages endpoint"),
		chatPath:     chatOK,
	})
	code, body := ask(t, h, "/v1/messages", messagesBody)
	if code != 200 || !strings.Contains(body, "from chat") || strings.Contains(body, "not accessible") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(messagesPath) != 1 || f.count(chatPath) != 1 {
		t.Fatalf("first calls: messages=%d chat=%d", f.count(messagesPath), f.count(chatPath))
	}
	if code, body = ask(t, h, "/v1/messages", messagesBody); code != 200 || !strings.Contains(body, "from chat") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(messagesPath) != 1 || f.count(chatPath) != 2 {
		t.Fatalf("second calls: messages=%d chat=%d", f.count(messagesPath), f.count(chatPath))
	}
}

// An Anthropic client on the translate path: the provider has no Messages
// API, Chat turns the model away, and the request goes on to Responses.
func TestAnthropicClientTranslateRefusedThenResponses(t *testing.T) {
	f, h := upstream(t, provider.Provider{Models: []string{"m1"}, Chat: "x", Responses: "x"}, map[string]scriptReply{
		chatPath:      refusal(chatRefusal),
		responsesPath: responsesOK,
	})
	code, body := ask(t, h, "/v1/messages", messagesBody)
	if code != 200 || !strings.Contains(body, "from responses") || strings.Contains(body, "not accessible") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
	if code, body = ask(t, h, "/v1/messages", messagesBody); code != 200 || !strings.Contains(body, "from responses") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count(chatPath), f.count(responsesPath))
	}
}

// A vendor-prefixed model name is looked up by its bare id in the model list.
func TestDeclaredAPIsVendorPrefix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("fake", "x", []catalog.Model{{ID: "claude-x", APIs: []string{"chat"}}}); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "fake", Chat: "x", Anthropic: "x"}
	chatOnly := []provider.Protocol{provider.Chat}
	for _, tc := range []struct {
		model string
		want  []provider.Protocol
	}{
		{"claude-x", chatOnly},
		{"openai/claude-x", chatOnly},
		{"vendor/sub/claude-x", chatOnly},
		{"other/gpt-y", nil},
	} {
		if got := declaredAPIs(p, tc.model); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("declaredAPIs(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
