package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// Acceptance checks V1–V4, V6 and V7, one case per item of the brief.

// accObj decodes a JSON object body, failing the test when it is not one.
func accObj(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, body)
	}
	return m
}

// accMap asserts v is a JSON object and returns it.
func accMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not a JSON object: %v", v)
	}
	return m
}

// accItems returns the objects of a list whose "type" is typ.
func accItems(list any, typ string) []map[string]any {
	var out []map[string]any
	arr, _ := list.([]any)
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok && m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

// accBlocks returns the content blocks of typ across a list of messages.
func accBlocks(msgs []any, typ string) []map[string]any {
	var out []map[string]any
	for _, mi := range msgs {
		m, _ := mi.(map[string]any)
		content, _ := m["content"].([]any)
		out = append(out, accItems(content, typ)...)
	}
	return out
}

// accStream splits a Gemini stream into the kinds of its parts (all events
// but the last, which carries finishReason and usage) and the function call.
func accStream(t *testing.T, body string) ([]string, map[string]any, map[string]any) {
	t.Helper()
	evs := events(body)
	if len(evs) < 2 {
		t.Fatalf("too few events: %s", body)
	}
	var kinds []string
	var call map[string]any
	for _, ev := range evs[:len(evs)-1] {
		cand := ev["candidates"].([]any)[0].(map[string]any)
		parts := cand["content"].(map[string]any)["parts"].([]any)
		if len(parts) != 1 {
			t.Fatalf("event should carry one part: %v", ev)
		}
		p := parts[0].(map[string]any)
		switch {
		case p["thought"] == true:
			kinds = append(kinds, "think")
		case p["functionCall"] != nil:
			kinds = append(kinds, "call")
			call = p["functionCall"].(map[string]any)
		case p["text"] != nil:
			kinds = append(kinds, "text")
		}
	}
	return kinds, call, evs[len(evs)-1]
}

// V1: generateContent answers in Gemini shape from every upstream protocol.
func TestAcceptV1GenerateContentFromEachUpstream(t *testing.T) {
	cases := []struct {
		name  string
		proto provider.Protocol
		reply string
		path  string
	}{
		{"chat", provider.Chat, sse(
			`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi there"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`), "/v1/chat/completions"},
		{"responses", provider.Responses, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
			`data: {"type":"response.output_text.delta","delta":"Hi there"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`),
			"/v1/responses"},
		{"anthropic", provider.Anthropic, sse(
			`data: {"type":"message_start","message":{"id":"m1","model":"m1","usage":{"input_tokens":10,"output_tokens":1}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi there"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`data: {"type":"message_stop"}`), "/v1/messages"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, reply: c.reply}
			setup(t, c.proto, f)
			code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiText)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if f.path != c.path {
				t.Errorf("upstream path %s, want %s", f.path, c.path)
			}
			if parts := geminiParts(t, body); len(parts) != 1 || parts[0]["text"] != "Hi there" {
				t.Errorf("parts: %v", parts)
			}
			if geminiFinishOf(t, body) != "STOP" {
				t.Errorf("finishReason: %s", body)
			}
			if !strings.Contains(body, `"usageMetadata":{`) || !strings.Contains(body, `"promptTokenCount":10`) {
				t.Errorf("usageMetadata: %s", body)
			}
		})
	}
}

// V2 (Responses upstream): thinking, text and a function call stream in
// order; the function call keeps its upstream id and full args; the last
// event carries finishReason and usage with thoughts.
func TestAcceptV2StreamResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`,
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_up_1","name":"read","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"a.go\"}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_up_1","name":"read","arguments":"{\"path\":\"a.go\"}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":10,"output_tokens":7,"output_tokens_details":{"reasoning_tokens":4}}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	kinds, call, last := accStream(t, body)
	if strings.Join(kinds, ",") != "think,text,call" {
		t.Errorf("order: %v", kinds)
	}
	if call == nil || call["id"] != "call_up_1" || call["name"] != "read" || accMap(t, call["args"])["path"] != "a.go" {
		t.Errorf("call: %v", call)
	}
	lastJSON, _ := json.Marshal(last)
	for _, want := range []string{`"finishReason":"STOP"`, `"thoughtsTokenCount":4`, `"promptTokenCount":10`} {
		if !strings.Contains(string(lastJSON), want) {
			t.Errorf("last event lacks %s: %s", want, lastJSON)
		}
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("events must end with a blank line: %q", body[max(0, len(body)-20):])
	}
}

// V2 (Anthropic upstream): same ordering and id guarantees from Anthropic's
// own stream events.
func TestAcceptV2StreamAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	kinds, call, last := accStream(t, body)
	if strings.Join(kinds, ",") != "think,text,call" {
		t.Errorf("order: %v", kinds)
	}
	if call == nil || call["id"] != "toolu_1" || accMap(t, call["args"])["path"] != "a.go" {
		t.Errorf("call: %v", call)
	}
	lastJSON, _ := json.Marshal(last)
	if !strings.Contains(string(lastJSON), `"finishReason":"STOP"`) || !strings.Contains(string(lastJSON), `"thoughtsTokenCount":`) {
		t.Errorf("last event: %s", lastJSON)
	}
}

const accTranslateReq = `{
  "systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[
    {"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1}}}`

// V3 (Responses upstream): system, lower-case schema, paired call and result,
// reasoning effort medium.
func TestAcceptV3TranslationToResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", accTranslateReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := accObj(t, string(f.got))
	if up["instructions"] != "be brief" {
		t.Errorf("instructions: %v", up["instructions"])
	}
	if accMap(t, up["reasoning"])["effort"] != "medium" {
		t.Errorf("reasoning: %v", up["reasoning"])
	}
	tool := accMap(t, accMap(t, up["tools"].([]any)[0]))
	params := accMap(t, tool["parameters"])
	props := accMap(t, accMap(t, params["properties"])["path"])
	if params["type"] != "object" || props["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	calls := accItems(up["input"], "function_call")
	outs := accItems(up["input"], "function_call_output")
	if len(calls) != 1 || len(outs) != 1 || calls[0]["call_id"] == "" || calls[0]["call_id"] != outs[0]["call_id"] {
		t.Errorf("call and result not paired: %v", up["input"])
	}
	if accMap(t, up["tool_choice"])["name"] != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}

// V3 (Anthropic upstream): system field, lower-case schema, paired tool_use
// and tool_result, thinking budget for medium.
func TestAcceptV3TranslationToAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"m1","model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", accTranslateReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := accObj(t, string(f.got))
	if up["system"] != "be brief" {
		t.Errorf("system: %v", up["system"])
	}
	if th := accMap(t, up["thinking"]); th["budget_tokens"] != float64(10000) {
		t.Errorf("thinking: %v", up["thinking"])
	}
	tool := accMap(t, up["tools"].([]any)[0])
	schema := accMap(t, tool["input_schema"])
	props := accMap(t, accMap(t, schema["properties"])["path"])
	if schema["type"] != "object" || props["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", schema)
	}
	msgs, _ := up["messages"].([]any)
	uses := accBlocks(msgs, "tool_use")
	results := accBlocks(msgs, "tool_result")
	if len(uses) != 1 || len(results) != 1 || uses[0]["id"] == "" || uses[0]["id"] != results[0]["tool_use_id"] {
		t.Errorf("tool_use and tool_result not paired: %v", msgs)
	}
	if tc := accMap(t, up["tool_choice"]); tc["type"] != "tool" || tc["name"] != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}

// V3 (Chat upstream): the same request, checked against the chat wire shape.
func TestAcceptV3TranslationToChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", accTranslateReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := accObj(t, string(f.got))
	msgs := upstreamMsgs(t, f.got)
	if len(msgs) < 1 || msgs[0]["role"] != "system" || msgs[0]["content"] != "be brief" {
		t.Errorf("system message: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort: %v", up["reasoning_effort"])
	}
	tool := accMap(t, up["tools"].([]any)[0])
	params := accMap(t, accMap(t, tool["function"])["parameters"])
	if params["type"] != "object" || accMap(t, accMap(t, params["properties"])["path"])["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
}

// V4: unknown model 404 in Gemini shape, countTokens, and the models list.
func TestAcceptV4ErrorsCountTokensAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", geminiText)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", geminiText)
	if m := accObj(t, body); code != 200 || m["totalTokens"] == nil {
		t.Errorf("countTokens: %d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	arr, _ := accObj(t, rec.Body.String())["models"].([]any)
	found := false
	for _, x := range arr {
		m := accMap(t, x)
		if m["name"] == "models/fake/m1" && len(m["supportedGenerationMethods"].([]any)) > 0 {
			found = true
		}
	}
	if rec.Code != 200 || !found {
		t.Errorf("models list: %d %s", rec.Code, rec.Body.String())
	}
}

// V4 (routing): a model name with a colon (ollama tag) keeps its tag, and the
// method is the text after the last colon.
func TestAcceptV4ModelWithColonRoutes(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)}
	up := setup(t, provider.Chat, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: up.URL + "/v1", Models: []string{"m1", "llama3:8b"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1beta/models/fake/llama3:8b:generateContent", geminiText)
	if code != 200 || geminiParts(t, body)[0]["text"] != "ok" {
		t.Fatalf("%d %s", code, body)
	}
	if up := accObj(t, string(f.got)); up["model"] != "llama3:8b" {
		t.Errorf("upstream model: %v", up["model"])
	}
}

// V6: a Codex login serves codex/<model> through Chat, Responses, Anthropic
// and Gemini interfaces alike.
func TestAcceptV6CodexModelFromEveryInterface(t *testing.T) {
	f := setupCodex(t)
	cases := []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`},
		{"anthropic", "/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"gemini", "/v1beta/models/codex/gpt-5.5:generateContent", geminiText},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			code, body := post(t, c.path, c.body)
			if code != 200 || !strings.Contains(body, "hello") {
				t.Fatalf("%d %s", code, body)
			}
			sent := accObj(t, string(f.got))
			if sent["model"] != "gpt-5.5" {
				t.Errorf("upstream model: %v", sent["model"])
			}
			for _, k := range []string{"max_output_tokens", "max_tokens", "temperature"} {
				if _, ok := sent[k]; ok {
					t.Errorf("%s reached Codex", k)
				}
			}
			if f.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(f.head.Get("Authorization"), "Bearer ") {
				t.Errorf("headers: %v", f.head)
			}
		})
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if !strings.Contains(rec.Body.String(), `"name":"models/codex/gpt-5.5"`) {
		t.Errorf("gemini models list lacks codex: %s", rec.Body.String())
	}
}

// V7: a non-streamed Responses ask gets one JSON object back; a streamed one
// is forwarded. Neither reaches Codex with the refused fields, and the
// upstream's missing Content-Type is handled.
func TestAcceptV7CodexResponsesNonStreamAndStream(t *testing.T) {
	f := setupCodex(t)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":false,"max_output_tokens":16,"temperature":0.2}`)
	m := accObj(t, body)
	if code != 200 || m["id"] != "resp_1" || m["status"] != "completed" || m["output"] == nil {
		t.Fatalf("non-stream: %d %s", code, body)
	}
	if strings.Contains(body, "response.created") {
		t.Errorf("events leaked into the JSON reply: %s", body)
	}
	sent := accObj(t, string(f.got))
	if sent["stream"] != true || sent["store"] != false {
		t.Errorf("upstream body: %s", f.got)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("%s reached Codex", k)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"))
	var login struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	json.Unmarshal(raw, &login)
	if f.head.Get("Authorization") != "Bearer "+login.Tokens.AccessToken || f.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("auth headers: %v", f.head)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi","top_p":0.5,"max_output_tokens":8,"temperature":0.1}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("stream: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("stream content type: %q", rec.Header().Get("Content-Type"))
	}
	for _, k := range []string{`"top_p"`, `"max_output_tokens"`, `"temperature"`} {
		if strings.Contains(string(f.got), k) {
			t.Errorf("%s reached Codex on a stream: %s", k, f.got)
		}
	}
	if !strings.Contains(string(f.got), `"stream":true`) {
		t.Errorf("stream not sent upstream: %s", f.got)
	}
}
