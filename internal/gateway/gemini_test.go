package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiChatBody is a Gemini CLI style request: a system instruction, a call
// from history with an id, a result matched by name, and a tool declaration
// with upper-case schema types.
const geminiChatBody = `{
  "systemInstruction": {"parts": [{"text": "be brief"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "read a.go"}]},
    {"role": "model", "parts": [{"functionCall": {"id": "t0", "name": "read", "args": {"path": "x"}}}]},
    {"role": "user", "parts": [{"functionResponse": {"name": "read", "response": {"output": "package x"}}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "read", "description": "read a file",
    "parameters": {"type": "OBJECT", "properties": {"path": {"type": "STRING"}}, "required": ["path"]}}]}],
  "generationConfig": {"maxOutputTokens": 100, "thinkingConfig": {"thinkingBudget": -1, "includeThoughts": true}}
}`

// chatUpstreamReply is what an OpenAI Chat upstream says: reasoning, text,
// one tool call streamed in fragments, then usage with 2 reasoning tokens.
var chatUpstreamReply = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`)

// geminiParts returns the parts of the first candidate of one response.
func geminiParts(ev map[string]any) []map[string]any {
	cands, _ := ev["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	cand, _ := cands[0].(map[string]any)
	content, _ := cand["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func finishOf(ev map[string]any) string {
	cands, _ := ev["candidates"].([]any)
	if len(cands) == 0 {
		return ""
	}
	cand, _ := cands[0].(map[string]any)
	s, _ := cand["finishReason"].(string)
	return s
}

func TestGeminiStreamOverChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatUpstreamReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiChatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages: %s", f.got)
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "t0" {
		t.Errorf("history call id not kept: %s", f.got)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "t0" {
		t.Errorf("result not matched to its call: %s", f.got)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) || up["stream"] != true {
		t.Errorf("params: %s", f.got)
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %s", f.got)
	}

	// what the client got: thought, text, a whole call, then the finish event
	evs := events(body)
	var kinds []string
	var texts string
	var call map[string]any
	for _, ev := range evs {
		for _, p := range geminiParts(ev) {
			switch {
			case p["thought"] == true:
				kinds = append(kinds, "thought")
				if p["text"] != "hmm" {
					t.Errorf("thought text: %v", p)
				}
			case p["functionCall"] != nil:
				kinds = append(kinds, "call")
				call = p["functionCall"].(map[string]any)
			case p["text"] != nil:
				kinds = append(kinds, "text")
				texts += p["text"].(string)
			}
		}
	}
	if got := strings.Join(kinds, " "); got != "thought text text call" {
		t.Errorf("part order: %s", got)
	}
	if texts != "Hi there" {
		t.Errorf("text: %q", texts)
	}
	if call["id"] != "call_1" || call["name"] != "read" || call["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("call: %v", call)
	}
	last := evs[len(evs)-1]
	if finishOf(last) != "STOP" {
		t.Errorf("finish: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateContentReturnsWholeReply(t *testing.T) {
	f := &fake{t: t, reply: chatUpstreamReply}
	up := setup(t, provider.Chat, f)
	// the catalog must hold a model whose id contains a colon
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: up.URL + "/v1", Models: []string{"m1", "m:1"}}); err != nil {
		t.Fatal(err)
	}
	// a model id with a colon: the method is what follows the last colon
	code, body := post(t, "/v1beta/models/fake/m:1:generateContent", geminiChatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(string(f.got), `"model":"m:1"`) {
		t.Errorf("model not resolved: %s", f.got)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	cand := out["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", cand["finishReason"])
	}
	var hasThought, hasCall bool
	var text string
	for _, p := range cand["content"].(map[string]any)["parts"].([]any) {
		m := p.(map[string]any)
		if m["thought"] == true {
			hasThought = m["text"] == "hmm"
		} else if c, ok := m["functionCall"].(map[string]any); ok {
			hasCall = c["id"] == "call_1" && c["name"] == "read" && c["args"].(map[string]any)["path"] == "a.go"
		} else if s, ok := m["text"].(string); ok {
			text += s
		}
	}
	if !hasThought || text != "Hi there" || !hasCall {
		t.Errorf("parts: %s", body)
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
	if out["responseId"] != "c1" || out["modelVersion"] != "m1" {
		t.Errorf("ids: %s", body)
	}
}

func TestGeminiJSONModeAndToolChoice(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"content":"{\"n\":1}"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"count"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}},{"name":"write","parameters":{"type":"OBJECT"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"},"type":{"type":"STRING"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	sys, _ := up["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, want := range []string{"valid JSON", `"type":"object"`, `"type":"integer"`, `"type":{"type":"string"}`} {
		if !strings.Contains(sys, want) {
			t.Errorf("system lacks %q: %s", want, sys)
		}
	}
	if strings.Contains(sys, "OBJECT") || strings.Contains(sys, "INTEGER") {
		t.Errorf("upper-case schema types reached upstream: %s", sys)
	}
	if tools := up["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools not limited to the allowed name: %s", f.got)
	}
	choice := up["tool_choice"].(map[string]any)["function"].(map[string]any)
	if choice["name"] != "read" {
		t.Errorf("tool_choice: %s", f.got)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	cand := out["candidates"].([]any)[0].(map[string]any)
	part := cand["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if part["text"] != `{"n":1}` || cand["finishReason"] != "STOP" {
		t.Errorf("reply: %s", body)
	}
}

func TestGeminiErrorsCountAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})

	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("unknown model: %d %s", code, body)
	}

	code, body = post(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}}`)
	var ct map[string]any
	json.Unmarshal([]byte(body), &ct)
	if n, _ := ct["totalTokens"].(float64); code != 200 || n <= 0 {
		t.Errorf("countTokens wrapped: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"direct"}]}]}`)
	if code != 200 || !strings.Contains(body, `"totalTokens"`) {
		t.Errorf("countTokens direct: %d %s", code, body)
	}

	code, body = post(t, "/v1beta/models/fake/m1:embedContent", `{}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown method: %d %s", code, body)
	}

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	list := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(list, `"name":"models/fake/m1"`) ||
		!strings.Contains(list, `"displayName"`) || !strings.Contains(list, `"streamGenerateContent"`) {
		t.Errorf("models: %d %s", rec.Code, list)
	}
}

func TestGeminiUsageMapping(t *testing.T) {
	u := geminiUsage(Usage{Input: 10, Output: 5, Reasoning: 2})
	if u["promptTokenCount"] != 10 || u["candidatesTokenCount"] != 3 || u["thoughtsTokenCount"] != 2 || u["totalTokenCount"] != 15 {
		t.Errorf("reasoning: %v", u)
	}
	if _, ok := u["cachedContentTokenCount"]; ok {
		t.Errorf("no cache, yet cachedContentTokenCount: %v", u)
	}
	// cache hits are part of the prompt count
	c := geminiUsage(Usage{Input: 4, CacheRead: 6, Output: 3})
	if c["promptTokenCount"] != 10 || c["cachedContentTokenCount"] != 6 || c["candidatesTokenCount"] != 3 {
		t.Errorf("cache: %v", c)
	}
}

func TestGeminiNeverSendsIncompleteCall(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &geminiEncoder{w: newSSEWriter(rec), model: "m1"}
	enc.event(Event{Kind: KToolStart, ID: "c", Name: "read"})
	enc.event(Event{Kind: KToolArgs, Text: `{"path":`})
	enc.event(Event{Kind: KStop, Stop: "length"})
	enc.finish()
	evs := events(rec.Body.String())
	for _, ev := range evs {
		for _, p := range geminiParts(ev) {
			if p["functionCall"] != nil {
				t.Errorf("incomplete call was sent: %s", rec.Body.String())
			}
		}
	}
	if last := evs[len(evs)-1]; finishOf(last) != "MAX_TOKENS" {
		t.Errorf("finish: %v", last)
	}
}

// anthropicThinkingReply is an Anthropic upstream that thinks with a
// signature, then answers in text.
var anthropicThinkingReply = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_2","model":"m1","usage":{"input_tokens":9}}}`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"deep"}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-2"}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
	`data: {"type":"content_block_stop","index":1}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
	`data: {"type":"message_stop"}`)

// Thinking with its signature survives the trip through an Anthropic
// upstream, in both directions: the signed history block is sent back, and
// the new signature reaches the client.
func TestGeminiThinkingSignatureRoundTrip(t *testing.T) {
	f := &fake{t: t, reply: anthropicThinkingReply}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "contents": [
	    {"role": "user", "parts": [{"text": "read x"}]},
	    {"role": "model", "parts": [{"text": "plan", "thought": true, "thoughtSignature": "sig-1"},
	                                {"functionCall": {"id": "t0", "name": "read", "args": {"path": "x"}}}]},
	    {"role": "user", "parts": [{"functionResponse": {"name": "read", "response": {"output": "ok"}}}]}
	  ],
	  "generationConfig": {"thinkingConfig": {"thinkingBudget": 2000, "includeThoughts": true}}
	}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	var up map[string]any
	json.Unmarshal(f.got, &up)
	var sent map[string]any
	for _, m := range up["messages"].([]any) {
		msg := m.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		for _, b := range msg["content"].([]any) {
			if blk := b.(map[string]any); blk["type"] == "thinking" {
				sent = blk
			}
		}
	}
	if sent == nil || sent["thinking"] != "plan" || sent["signature"] != "sig-1" {
		t.Errorf("signed thinking not sent back upstream: %s", f.got)
	}

	var sigs []string
	var deep bool
	for _, ev := range events(body) {
		for _, p := range geminiParts(ev) {
			if p["thought"] != true {
				continue
			}
			if s, ok := p["thoughtSignature"].(string); ok {
				sigs = append(sigs, s)
			}
			if p["text"] == "deep" {
				deep = true
			}
		}
	}
	if !deep || len(sigs) != 1 || sigs[0] != "sig-2" {
		t.Errorf("thinking or signature missing from the stream: %s", body)
	}
}

// Without includeThoughts the budget still sets the upstream's effort, but
// no reasoning text goes back to the client, in either form.
func TestGeminiHidesThoughtsUnlessAsked(t *testing.T) {
	const body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`
	for _, path := range []string{"generateContent", "streamGenerateContent?alt=sse"} {
		f := &fake{t: t, reply: chatUpstreamReply}
		setup(t, provider.Chat, f)
		code, out := post(t, "/v1beta/models/fake/m1:"+path, body)
		if code != 200 {
			t.Fatalf("%s: status %d: %s", path, code, out)
		}
		var up map[string]any
		json.Unmarshal(f.got, &up)
		if up["reasoning_effort"] != "medium" {
			t.Errorf("%s: budget -1 did not reach upstream as medium: %s", path, f.got)
		}
		if strings.Contains(out, `"thought":true`) || strings.Contains(out, "hmm") {
			t.Errorf("%s: reasoning returned without includeThoughts: %s", path, out)
		}
		// streamed, the text comes in pieces ("Hi " then "there")
		if !strings.Contains(out, "Hi ") || !strings.Contains(out, "there") {
			t.Errorf("%s: text lost: %s", path, out)
		}
	}
}

// A reply that names no model still says which model answered: the one the
// client asked for.
func TestGeminiModelVersionFromRequest(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c3","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c3","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out["modelVersion"] != "fake/m1" {
		t.Errorf("generateContent modelVersion: %d %s", code, body)
	}

	f2 := &fake{t: t, reply: f.reply}
	setup(t, provider.Chat, f2)
	code, body = post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	evs := events(body)
	if code != 200 || len(evs) == 0 || evs[len(evs)-1]["modelVersion"] != "fake/m1" {
		t.Errorf("stream modelVersion: %d %s", code, body)
	}
}

// A result with no id and no call of the same name to answer must not reach
// the upstream with an empty tool_call_id; it travels as text instead.
func TestGeminiOrphanResultIsText(t *testing.T) {
	f := &fake{t: t, reply: chatUpstreamReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"x"}}}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(string(f.got), "tool_call_id") {
		t.Errorf("orphan result sent as a tool result: %s", f.got)
	}
	if !strings.Contains(string(f.got), "[result of read] x") {
		t.Errorf("orphan result text lost: %s", f.got)
	}
}
