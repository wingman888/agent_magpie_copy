package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// gemTestEvents splits an SSE body into the JSON objects of its data lines.
func gemTestEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(block), "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		out = append(out, m)
	}
	return out
}

// gemTestParts returns the parts of the first candidate of a Gemini body.
func gemTestParts(m map[string]any) []map[string]any {
	cands, _ := m["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	content, _ := cands[0].(map[string]any)["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func gemTestParse(t *testing.T, body string, stream bool) *Request {
	t.Helper()
	r, err := parseGemini([]byte(body), "fake/m1", stream)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const gemTestContents = `{"role":"user","parts":[{"text":"hi"}]}`

func TestGeminiCallIDsEchoedAndMatchedByName(t *testing.T) {
	r := gemTestParse(t, `{"contents":[
		{"role":"user","parts":[{"text":"go"}]},
		{"role":"model","parts":[
			{"functionCall":{"id":"client-1","name":"search","args":{"q":"a"}}},
			{"functionCall":{"name":"read","args":{"path":"x"}}}]},
		{"role":"user","parts":[
			{"functionResponse":{"name":"read","response":{"output":"file body"}}},
			{"functionResponse":{"id":"client-1","name":"search","response":{"output":"hits"}}}]}
	]}`, false)
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(r.Messages))
	}
	calls := r.Messages[1].Parts
	if calls[0].Kind != ToolCall || calls[0].ID != "client-1" {
		t.Fatalf("client id not kept: %+v", calls[0])
	}
	if calls[1].ID == "" || calls[1].ID == "client-1" {
		t.Fatalf("generated id = %q", calls[1].ID)
	}
	res := r.Messages[2].Parts
	if res[0].Kind != ToolResult || res[0].CallID != calls[1].ID || res[0].Text != "file body" {
		t.Fatalf("result matched by name wrong: %+v", res[0])
	}
	if res[1].CallID != "client-1" || res[1].Text != "hits" {
		t.Fatalf("result with id wrong: %+v", res[1])
	}
}

func TestGeminiUpperCaseSchemaLowered(t *testing.T) {
	r := gemTestParse(t, `{"contents":[`+gemTestContents+`],
		"tools":[{"functionDeclarations":[{"name":"f","description":"d","parameters":
			{"type":"OBJECT","properties":{
				"type":{"type":"STRING","enum":["A"]},
				"items":{"type":"ARRAY","items":{"type":"INTEGER"}}},
			 "required":["type"]}}]}]}`, false)
	if len(r.Tools) != 1 {
		t.Fatalf("tools = %d", len(r.Tools))
	}
	var s map[string]any
	if err := json.Unmarshal(r.Tools[0].Schema, &s); err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(map[string]any)
	if s["type"] != "object" {
		t.Fatalf("root type = %v", s["type"])
	}
	if props["type"].(map[string]any)["type"] != "string" {
		t.Fatalf("property named type lost its type: %v", props["type"])
	}
	if props["items"].(map[string]any)["type"] != "array" {
		t.Fatalf("property named items: %v", props["items"])
	}
	if props["items"].(map[string]any)["items"].(map[string]any)["type"] != "integer" {
		t.Fatalf("nested items type not lowered")
	}
	if req := s["required"].([]any); len(req) != 1 || req[0] != "type" {
		t.Fatalf("required changed: %v", req)
	}
	out := string(buildChat(r, "m1", "api.example.com"))
	if strings.Contains(out, "OBJECT") || strings.Contains(out, "STRING") || strings.Contains(out, "INTEGER") {
		t.Fatalf("upper-case schema reached upstream: %s", out)
	}
}

func TestGeminiToolChoiceMapping(t *testing.T) {
	cases := []struct{ cfg, want string }{
		{`{"mode":"NONE"}`, "none"},
		{`{"mode":"AUTO"}`, "auto"},
		{`{"mode":"ANY","allowedFunctionNames":["f"]}`, "name:f"},
		{`{"mode":"ANY"}`, "required"},
		{`{"mode":"VALIDATED"}`, ""},
	}
	for _, c := range cases {
		r := gemTestParse(t, `{"contents":[`+gemTestContents+`],"toolConfig":{"functionCallingConfig":`+c.cfg+`}}`, false)
		if r.ToolChoice != c.want {
			t.Errorf("%s: ToolChoice = %q, want %q", c.cfg, r.ToolChoice, c.want)
		}
	}
}

func TestGeminiGenerationConfigAndThinking(t *testing.T) {
	r := gemTestParse(t, `{"contents":[`+gemTestContents+`],"generationConfig":{
		"maxOutputTokens":123,"temperature":0.5,"stopSequences":["END"],
		"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`, true)
	if r.MaxTokens != 123 {
		t.Fatalf("MaxTokens = %d, want 123", r.MaxTokens)
	}
	if r.Effort != "medium" || !r.Thinking || !r.Stream {
		t.Fatalf("effort=%q thinking=%v stream=%v", r.Effort, r.Thinking, r.Stream)
	}
	if len(r.Stop) != 1 || r.Stop[0] != "END" {
		t.Fatalf("stop = %v", r.Stop)
	}
	out := string(buildChat(r, "m1", "api.example.com"))
	if !strings.Contains(out, `"max_tokens":123`) {
		t.Fatalf("max tokens not sent upstream: %s", out)
	}
	if !strings.Contains(out, `"reasoning_effort":"medium"`) {
		t.Fatalf("budget -1 not medium upstream: %s", out)
	}

	off := gemTestParse(t, `{"contents":[`+gemTestContents+`],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, false)
	if off.Effort != "" || off.Thinking {
		t.Fatalf("budget 0 should disable thinking: effort=%q thinking=%v", off.Effort, off.Thinking)
	}
}

func TestGeminiJSONModeAsksForSchema(t *testing.T) {
	r := gemTestParse(t, `{"contents":[`+gemTestContents+`],"systemInstruction":{"parts":[{"text":"be terse"}]},
		"generationConfig":{"responseMimeType":"application/json",
			"responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`, false)
	if !strings.HasPrefix(r.System, "be terse") {
		t.Fatalf("system lost its text: %q", r.System)
	}
	if !strings.Contains(r.System, "only") || !strings.Contains(r.System, `"type":"object"`) || strings.Contains(r.System, "OBJECT") {
		t.Fatalf("JSON instruction missing or schema not lowered: %q", r.System)
	}
}

func TestGeminiMessagesImagesAndAttachments(t *testing.T) {
	r := gemTestParse(t, `{"contents":[{"role":"user","parts":[
		{"inlineData":{"mimeType":"image/png","data":"AAAA"}},
		{"inlineData":{"mimeType":"application/pdf","data":"BBBB"}},
		{"text":"what is this"}]}]}`, false)
	parts := r.Messages[0].Parts
	if parts[0].Kind != Image || parts[0].MediaType != "image/png" || parts[0].Data != "AAAA" {
		t.Fatalf("image not kept: %+v", parts[0])
	}
	if parts[1].Kind != Text || !strings.Contains(parts[1].Text, "application/pdf") {
		t.Fatalf("attachment placeholder missing: %+v", parts[1])
	}
	out := string(buildChat(r, "m1", "api.example.com"))
	if !strings.Contains(out, "data:image/png;base64,AAAA") {
		t.Fatalf("image not sent upstream: %s", out)
	}
}

func TestGeminiUsageCounts(t *testing.T) {
	res := Result{ID: "r1", Parts: []Part{{Kind: Text, Text: "ok"}}, Stop: "stop",
		Usage: Usage{Input: 10, Output: 5, Reasoning: 2}}
	var m map[string]any
	if err := json.Unmarshal(renderGemini(res, "fake/m1"), &m); err != nil {
		t.Fatal(err)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) ||
		u["thoughtsTokenCount"] != float64(2) || u["totalTokenCount"] != float64(15) {
		t.Fatalf("usage = %v", u)
	}
	if m["responseId"] != "r1" || m["modelVersion"] != "fake/m1" {
		t.Fatalf("ids = %v %v", m["responseId"], m["modelVersion"])
	}
	c := m["candidates"].([]any)[0].(map[string]any)
	if c["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", c["finishReason"])
	}
}

func TestGeminiFinishReasons(t *testing.T) {
	cases := map[string]string{"stop": "STOP", "tool": "STOP", "length": "MAX_TOKENS", "filter": "SAFETY"}
	for stop, want := range cases {
		m := map[string]any{}
		json.Unmarshal(renderGemini(Result{Stop: stop}, "m"), &m)
		c := m["candidates"].([]any)[0].(map[string]any)
		if c["finishReason"] != want {
			t.Errorf("stop %q -> %v, want %s", stop, c["finishReason"], want)
		}
	}
}

func TestGeminiRenderKeepsCallWhole(t *testing.T) {
	res := Result{Stop: "tool", Parts: []Part{
		{Kind: Thinking, Text: "hmm"},
		{Kind: ToolCall, ID: "c1", Name: "f", Args: json.RawMessage(`{"a":1}`)},
	}}
	var m map[string]any
	json.Unmarshal(renderGemini(res, "m"), &m)
	parts := gemTestParts(m)
	if len(parts) != 2 || parts[0]["thought"] != true {
		t.Fatalf("parts = %v", parts)
	}
	fc := parts[1]["functionCall"].(map[string]any)
	if fc["id"] != "c1" || fc["name"] != "f" || fc["args"].(map[string]any)["a"] != float64(1) {
		t.Fatalf("functionCall = %v", fc)
	}
}

func TestGeminiStreamToolCallWhole(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := newGeminiEncoder(newSSEWriter(rec), "fake/m1")
	enc.event(Event{Kind: KStart, MsgID: "up-1"})
	enc.event(Event{Kind: KToolStart, ID: "call_9", Name: "f"})
	enc.event(Event{Kind: KToolArgs, Text: `{"a":`})
	if strings.Contains(rec.Body.String(), "functionCall") {
		t.Fatalf("partial call sent before its arguments were whole: %s", rec.Body.String())
	}
	enc.event(Event{Kind: KToolArgs, Text: `1}`})
	enc.event(Event{Kind: KStop, Stop: "tool"})
	enc.event(Event{Kind: KUsage, Usage: Usage{Input: 10, Output: 5, Reasoning: 2}})
	enc.finish()

	evs := gemTestEvents(t, rec.Body.String())
	var calls []map[string]any
	for _, ev := range evs {
		for _, p := range gemTestParts(ev) {
			if fc, ok := p["functionCall"].(map[string]any); ok {
				calls = append(calls, fc)
			}
		}
	}
	if len(calls) != 1 {
		t.Fatalf("function calls sent = %d, want 1: %s", len(calls), rec.Body.String())
	}
	if calls[0]["id"] != "call_9" || calls[0]["args"].(map[string]any)["a"] != float64(1) {
		t.Fatalf("call = %v", calls[0])
	}
	last := evs[len(evs)-1]
	c := last["candidates"].([]any)[0].(map[string]any)
	u := last["usageMetadata"].(map[string]any)
	if c["finishReason"] != "STOP" || u["promptTokenCount"] != float64(10) ||
		u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Fatalf("final event = %v", last)
	}
	if last["responseId"] != "up-1" {
		t.Fatalf("responseId = %v", last["responseId"])
	}
}

// Full path: a Chat Completions upstream stream goes through the decoder and
// comes out as Gemini events. The call's id and arguments survive intact.
func TestGeminiChatUpstreamStream(t *testing.T) {
	lines := []string{
		`{"id":"up1","model":"m1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_up1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	}
	rec := httptest.NewRecorder()
	enc := newGeminiEncoder(newSSEWriter(rec), "fake/m1")
	dec := (&chatDecoder{}).decode
	for _, l := range lines {
		if err := dec(l, enc.event); err != nil {
			t.Fatal(err)
		}
	}
	enc.finish()

	evs := gemTestEvents(t, rec.Body.String())
	var parts []map[string]any
	for _, ev := range evs {
		parts = append(parts, gemTestParts(ev)...)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3 (thought, text, call): %s", len(parts), rec.Body.String())
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "think" {
		t.Fatalf("thought part = %v", parts[0])
	}
	if parts[1]["text"] != "Hello" {
		t.Fatalf("text part = %v", parts[1])
	}
	fc := parts[2]["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_up1" || fc["name"] != "get_weather" || args["city"] != "Paris" {
		t.Fatalf("call = %v", fc)
	}
	last := evs[len(evs)-1]
	c := last["candidates"].([]any)[0].(map[string]any)
	if c["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %v", c["finishReason"])
	}
	u := last["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Fatalf("usage = %v", u)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	n, err := geminiCountTokens([]byte(`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"abcdefghijklmnop"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("tokens = %d, want 4", n)
	}
	n, err = geminiCountTokens([]byte(`{"contents":[{"role":"user","parts":[{"text":"abcdefghijklmnop"}]}]}`))
	if err != nil || n != 4 {
		t.Fatalf("direct form: n=%d err=%v", n, err)
	}
}

func TestGeminiErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGeminiError(rec, 404, "unknown model x")
	var m map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	e := m["error"]
	if rec.Code != 404 || e["code"] != float64(404) || e["status"] != "NOT_FOUND" || e["message"] != "unknown model x" {
		t.Fatalf("code=%d body=%v", rec.Code, m)
	}
}
