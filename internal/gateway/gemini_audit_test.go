package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// TestAuditGeminiThoughtGoesBackToChat: a thought in the Gemini history is
// sent back to a Chat upstream as the assistant's reasoning_content.
func TestAuditGeminiThoughtGoesBackToChat(t *testing.T) {
	f := &fake{t: t, reply: chatText("ok")}
	setup(t, provider.Chat, f)
	body := `{"contents":[
	  {"role":"user","parts":[{"text":"q"}]},
	  {"role":"model","parts":[{"thought":true,"text":"plan"},{"text":"answer"}]},
	  {"role":"user","parts":[{"text":"again"}]}]}`
	if code, b := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("status %d: %s", code, b)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages: %s", f.got)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["reasoning_content"] != "plan" || asst["content"] != "answer" {
		t.Errorf("thought not sent back: %s", f.got)
	}
}

// TestAuditChatClientHistoryUnchangedOnPlainUpstream: a Chat client's own
// reasoning_content is still left out for an upstream that is not DeepSeek.
func TestAuditChatClientHistoryUnchangedOnPlainUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatText("ok")}
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a","reasoning_content":"old"},{"role":"user","content":"again"}]}`
	if code, b := post(t, "/v1/chat/completions", body); code != 200 {
		t.Fatalf("status %d: %s", code, b)
	}
	if strings.Contains(string(f.got), "reasoning_content") {
		t.Errorf("a non-Gemini Chat request changed: %s", f.got)
	}
}

// TestAuditGeminiThoughtSignatureToAnthropic: a signed thought goes back as a
// thinking block carrying its signature.
func TestAuditGeminiThoughtSignatureToAnthropic(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg1","model":"m1","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	body := `{"contents":[
	  {"role":"user","parts":[{"text":"q"}]},
	  {"role":"model","parts":[{"thought":true,"text":"plan","thoughtSignature":"sig1"},{"text":"answer"}]},
	  {"role":"user","parts":[{"text":"again"}]}]}`
	if code, b := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("status %d: %s", code, b)
	}
	got := string(f.got)
	if !strings.Contains(got, `"type":"thinking"`) || !strings.Contains(got, `"signature":"sig1"`) || !strings.Contains(got, `"thinking":"plan"`) {
		t.Errorf("signed thought not sent back: %s", got)
	}
}

// TestAuditGeminiReplyMetadata: a Gemini reply names the model and carries a response id.
func TestAuditGeminiReplyMetadata(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: chatText("ok")})
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	id, _ := m["responseId"].(string)
	if code != 200 || m["modelVersion"] != "m1" || id == "" {
		t.Errorf("%d: modelVersion=%v responseId=%v", code, m["modelVersion"], m["responseId"])
	}
}

// TestAuditGeminiToolChoiceModes: ANY with several names is "required", AUTO is "auto".
func TestAuditGeminiToolChoiceModes(t *testing.T) {
	cases := []struct{ cfg, want string }{
		{`{"mode":"ANY","allowedFunctionNames":["a","b"]}`, "required"},
		{`{"mode":"ANY"}`, "required"},
		{`{"mode":"AUTO"}`, "auto"},
	}
	for _, c := range cases {
		f := &fake{t: t, reply: chatText("ok")}
		setup(t, provider.Chat, f)
		body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		  "tools":[{"functionDeclarations":[{"name":"a","parameters":{"type":"OBJECT"}},{"name":"b","parameters":{"type":"OBJECT"}}]}],
		  "toolConfig":{"functionCallingConfig":` + c.cfg + `}}`
		if code, b := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
			t.Fatalf("status %d: %s", code, b)
		}
		var up map[string]any
		json.Unmarshal(f.got, &up)
		if up["tool_choice"] != c.want {
			t.Errorf("%s: tool_choice = %v, want %s", c.cfg, up["tool_choice"], c.want)
		}
	}
}
