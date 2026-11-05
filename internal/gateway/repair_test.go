package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A stream whose first event says the model is not served on the API asked
// moves on before any content goes out, as a 400 refusal does.
func TestStreamRefusalBeforeContentMovesOn(t *testing.T) {
	refusal := sse(`data: {"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`)
	for _, tc := range []struct {
		name    string
		client  string
		body    string
		first   string
		second  string
		replies map[string]apiReply
		want    string
	}{
		{"chat client", pathChat, chatReqBody, pathChat, pathResp,
			map[string]apiReply{pathChat: {200, "text/event-stream", refusal}, pathResp: respOK}, "fallback ok"},
		{"responses client", pathResp, respReqBody, pathResp, pathChat,
			map[string]apiReply{pathResp: {200, "text/event-stream", sse(
				`event: response.failed` + "\n" + `data: {"type":"response.failed","response":{"error":{"code":"unsupported_api_for_model","message":"model m1 is not accessible via the /chat/completions endpoint"}}}`)},
				pathChat: chatOK}, "chat ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIFake(tc.replies)
			apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Responses: "x"}, nil)
			code, body := postTo(New().Handler(), tc.client, tc.body)
			if code != 200 || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", code, body)
			}
			if f.count(tc.first) != 1 || f.count(tc.second) != 1 {
				t.Fatalf("calls: first=%d second=%d", f.count(tc.first), f.count(tc.second))
			}
		})
	}
}

// An error event in a stream that is not a refusal still reaches the agent.
func TestStreamErrorBeforeContentThatIsNotARefusalStays(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathChat: {200, "text/event-stream", sse(`data: {"error":{"message":"server exploded"}}`)},
		pathResp: respOK,
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Responses: "x"}, nil)
	code, body := postTo(New().Handler(), pathChat, chatReqBody)
	if !strings.Contains(body, "server exploded") || f.count(pathResp) != 0 {
		t.Fatalf("%d %s, responses calls %d", code, body, f.count(pathResp))
	}
}

// Retry-After on every 503 still makes at most three requests, and the third
// failure is delivered. Zero keeps the test from waiting.
func TestRetryAfterDoesNotBypassTheRetryLimit(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{503, "0", `{"error":{"message":"busy"}}`}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	s := New()
	code, body := postTo(s.Handler(), "/v1/chat/completions", soloChatReq)
	if code != 503 || !strings.Contains(body, "busy") || up.count() != 3 {
		t.Fatalf("%d %s after %d requests", code, body, up.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 {
		t.Fatalf("tries %+v", r.Tries)
	}
}

// Copilot's open-weight gpt-oss models are not OpenAI's: Chat first.
func TestCopilotGptOssIsNotOpenAIs(t *testing.T) {
	copilot := provider.Provider{ID: "copilot", Account: &provider.Account{Agent: "copilot"},
		Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	for _, m := range []string{"openai/gpt-oss-120b", "gpt-oss-20b"} {
		if got := translationOrder(copilot, m); !slices.Equal(got, chatFirst) {
			t.Errorf("%s: %v, want %v", m, got, chatFirst)
		}
	}
}

// Without a live model list the model's Anthropic API is unknown, so
// count_tokens is not forwarded.
func TestCountTokensNotForwardedWhenListUnknown(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathCount: {200, "application/json", `{"input_tokens":7}`},
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"claude-x"}, Anthropic: "x"}, nil)
	code, _ := postTo(New().Handler(), "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || f.count(pathCount) != 0 {
		t.Fatalf("status %d, upstream count calls %d", code, f.count(pathCount))
	}
}
