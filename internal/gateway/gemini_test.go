package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemSrvJSON decodes a JSON object or fails the test.
func gemSrvJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, b)
	}
	return m
}

// gemSrvObj is m[key] as an object, failing the test when it is not one.
func gemSrvObj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want object", key, m[key])
	}
	return o
}

// gemSrvList is m[key] as a list, failing the test when it is not one.
func gemSrvList(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	l, ok := m[key].([]any)
	if !ok || len(l) == 0 {
		t.Fatalf("%s is %T (%v), want non-empty list", key, m[key], m[key])
	}
	return l
}

func gemSrvGet(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// gemSrvChatText is a Chat upstream reply: two text deltas, then usage.
func gemSrvChatText() string {
	return sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)
}

// gemSrvPlain is a small request with one user turn.
const gemSrvPlain = `{"contents":[{"role":"user","parts":[{"text":"hello there, count these tokens please"}]}]}`

func TestGemSrvGenerateContent(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", gemSrvPlain)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := gemSrvJSON(t, []byte(body))
	cand := gemSrvList(t, m, "candidates")[0].(map[string]any)
	parts := gemSrvList(t, gemSrvObj(t, cand, "content"), "parts")
	if got := parts[0].(map[string]any)["text"]; got != "Hi there" {
		t.Fatalf("text = %v, want %q", got, "Hi there")
	}
	if cand["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", cand["finishReason"])
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Fatalf("reply is not compact: %s", body)
	}
	um := gemSrvObj(t, m, "usageMetadata")
	if um["promptTokenCount"] != 10.0 || um["candidatesTokenCount"] != 5.0 {
		t.Fatalf("usageMetadata = %v", um)
	}
	if _, ok := m["modelVersion"]; !ok {
		t.Fatalf("no modelVersion: %s", body)
	}
	if _, ok := m["responseId"]; !ok {
		t.Fatalf("no responseId: %s", body)
	}
}

func TestGemSrvStreamOrder(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", gemSrvPlain)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 5 {
		t.Fatalf("got %d events, want 5 (thought, text, text, call, final): %s", len(evs), body)
	}
	part := func(i int) map[string]any {
		cand := gemSrvList(t, evs[i], "candidates")[0].(map[string]any)
		return gemSrvList(t, gemSrvObj(t, cand, "content"), "parts")[0].(map[string]any)
	}
	if p := part(0); p["thought"] != true || p["text"] != "hmm" {
		t.Fatalf("event 0 = %v, want thought hmm", p)
	}
	if p := part(1); p["text"] != "Hi " || p["thought"] != nil {
		t.Fatalf("event 1 = %v, want text Hi ", p)
	}
	if p := part(2); p["text"] != "there" {
		t.Fatalf("event 2 = %v, want text there", p)
	}
	fc := gemSrvObj(t, part(3), "functionCall")
	if fc["id"] != "call_1" || fc["name"] != "read" {
		t.Fatalf("functionCall = %v", fc)
	}
	if args := gemSrvObj(t, fc, "args"); args["path"] != "a.go" {
		t.Fatalf("args = %v, want the full arguments", args)
	}
	last := evs[4]
	cand := gemSrvList(t, last, "candidates")[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Fatalf("final finishReason = %v", cand["finishReason"])
	}
	um := gemSrvObj(t, last, "usageMetadata")
	if um["thoughtsTokenCount"] != 2.0 || um["promptTokenCount"] != 10.0 {
		t.Fatalf("final usageMetadata = %v", um)
	}
}

func TestGemSrvTranslatesRequestToChat(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":321,"temperature":0.5,"stopSequences":["END"],"thinkingConfig":{"thinkingBudget":-1}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := gemSrvJSON(t, f.got)
	msgs := gemSrvList(t, up, "messages")
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Fatalf("first message = %v, want system with plain text", sys)
	}
	asst := msgs[2].(map[string]any)
	calls := gemSrvList(t, asst, "tool_calls")
	id := calls[0].(map[string]any)["id"]
	if asst["role"] != "assistant" || id == "" || id == nil {
		t.Fatalf("assistant = %v, want a tool_calls message with an id", asst)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != id || tool["content"] != "package x" {
		t.Fatalf("tool message = %v, want role tool paired with %v", tool, id)
	}
	tools := gemSrvList(t, up, "tools")
	params := gemSrvObj(t, tools[0].(map[string]any)["function"].(map[string]any), "parameters")
	if params["type"] != "object" || gemSrvObj(t, params, "properties")["path"].(map[string]any)["type"] != "string" {
		t.Fatalf("parameters = %v, want lower-case types", params)
	}
	if up["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort = %v, want medium for budget -1", up["reasoning_effort"])
	}
	tc := gemSrvObj(t, up, "tool_choice")
	if gemSrvObj(t, tc, "function")["name"] != "read" {
		t.Fatalf("tool_choice = %v, want the single allowed function", tc)
	}
	if up["max_tokens"] != 321.0 && up["max_completion_tokens"] != 321.0 {
		t.Fatalf("max tokens missing: %v", up)
	}
	if up["temperature"] != 0.5 {
		t.Fatalf("temperature = %v", up["temperature"])
	}
	if stop := gemSrvList(t, up, "stop"); stop[0] != "END" {
		t.Fatalf("stop = %v", stop)
	}
}

func TestGemSrvKeepsCallIDsAndPairsByName(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[
	  {"role":"model","parts":[{"functionCall":{"id":"fc_9","name":"read","args":{"path":"a"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"id":"fc_9","name":"read","response":{"output":"A"}}}]},
	  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"b"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"B"}}}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	msgs := gemSrvList(t, gemSrvJSON(t, f.got), "messages")
	first := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if first["id"] != "fc_9" {
		t.Fatalf("call with an id got %v, want fc_9", first["id"])
	}
	if msgs[1].(map[string]any)["tool_call_id"] != "fc_9" {
		t.Fatalf("response with an id = %v", msgs[1])
	}
	second := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	if second == "" || second == "fc_9" {
		t.Fatalf("call without an id got %v, want a fresh id", second)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != second {
		t.Fatalf("response without an id = %v, want it paired with %v", msgs[3], second)
	}
}

func TestGemSrvNoneToolChoice(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`)
	if up := gemSrvJSON(t, f.got); up["tool_choice"] != "none" {
		t.Fatalf("tool_choice = %v, want none", up["tool_choice"])
	}
}

func TestGemSrvJSONModeUsesSystemPrompt(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}}}`)
	up := gemSrvJSON(t, f.got)
	if _, ok := up["response_format"]; ok {
		t.Fatalf("upstream got a response_format field: %v", up)
	}
	sys := gemSrvList(t, up, "messages")[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "JSON") || !strings.Contains(sys, `"city"`) || !strings.Contains(sys, `"type":"string"`) {
		t.Fatalf("system prompt = %q, want JSON instruction with the lower-case schema", sys)
	}
}

func TestGemSrvAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":100}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := gemSrvJSON(t, f.got)
	if up["max_tokens"] != 100.0 || up["system"] != "be brief" {
		t.Fatalf("upstream body = %v, want max_tokens and system", up)
	}
	m := gemSrvJSON(t, []byte(body))
	cand := gemSrvList(t, m, "candidates")[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", cand["finishReason"])
	}
	if p := gemSrvList(t, gemSrvObj(t, cand, "content"), "parts")[0].(map[string]any); p["text"] != "Hi" {
		t.Fatalf("text = %v", p)
	}
}

func TestGemSrvCountTokens(t *testing.T) {
	f := &fake{t: t, reply: gemSrvChatText()}
	setup(t, provider.Chat, f)
	code, bare := post(t, "/v1beta/models/fake/m1:countTokens", gemSrvPlain)
	if code != 200 || !strings.Contains(bare, `"totalTokens":`) {
		t.Fatalf("bare: status %d: %s", code, bare)
	}
	code, wrapped := post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":`+gemSrvPlain+`}`)
	if code != 200 || wrapped != bare {
		t.Fatalf("wrapped: status %d, %s; want %s", code, wrapped, bare)
	}
	if f.got != nil {
		t.Fatalf("countTokens reached the upstream: %s", f.got)
	}
}

func TestGemSrvUnknownModelAndMethod(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: gemSrvChatText()})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", gemSrvPlain)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("unknown model: status %d: %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:embedContent", gemSrvPlain)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("unknown method: status %d: %s", code, body)
	}
}

func TestGemSrvListsModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: gemSrvChatText()})
	code, body := gemSrvGet(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := gemSrvJSON(t, []byte(body))
	models := gemSrvList(t, m, "models")
	first := models[0].(map[string]any)
	if first["name"] != "models/fake/m1" {
		t.Fatalf("name = %v, want models/fake/m1", first["name"])
	}
	methods := gemSrvList(t, first, "supportedGenerationMethods")
	if methods[0] != "generateContent" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestGemSrvInfoAndNotFoundMentionGemini(t *testing.T) {
	code, body := gemSrvGet(t, "/")
	if code != 200 || !strings.Contains(body, "v1beta") || !strings.Contains(body, "generateContent") {
		t.Fatalf("info: status %d: %s", code, body)
	}
	code, body = gemSrvGet(t, "/nowhere")
	if code != 404 || !strings.Contains(body, "v1beta") {
		t.Fatalf("404: status %d: %s", code, body)
	}
}
