package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// gemTestMap decodes a JSON object for inspection.
func gemTestMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

// gemTestSSE reads the data lines of a Gemini SSE body.
func gemTestSSE(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if !strings.HasPrefix(block, "data: ") {
			continue
		}
		out = append(out, gemTestMap(t, []byte(strings.TrimPrefix(block, "data: "))))
	}
	return out
}

// gemTestCand returns candidates[0] of a Gemini response.
func gemTestCand(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cs, ok := m["candidates"].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("no candidates in %v", m)
	}
	return cs[0].(map[string]any)
}

// gemTestParts returns the parts of candidates[0].content.
func gemTestParts(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	content := gemTestCand(t, m)["content"].(map[string]any)
	var out []map[string]any
	for _, p := range content["parts"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiSchemaUppercaseBecomesLowercase(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	"tools":[{"functionDeclarations":[{"name":"get_weather","description":"d",
	"parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"},
	"days":{"type":"ARRAY","items":{"type":"INTEGER"}},"type":{"type":"BOOLEAN"}},"required":["city"]}}]}]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(r.Tools))
	}
	s := gemTestMap(t, r.Tools[0].Schema)
	if s["type"] != "object" {
		t.Errorf("top type = %v, want object", s["type"])
	}
	props := s["properties"].(map[string]any)
	if props["city"].(map[string]any)["type"] != "string" {
		t.Errorf("city type not lowercased: %v", props["city"])
	}
	days := props["days"].(map[string]any)
	if days["type"] != "array" || days["items"].(map[string]any)["type"] != "integer" {
		t.Errorf("days schema not lowercased: %v", days)
	}
	if props["type"].(map[string]any)["type"] != "boolean" {
		t.Errorf("a property named type must keep its schema and lowercase it: %v", props["type"])
	}
	if req, _ := s["required"].([]any); len(req) != 1 || req[0] != "city" {
		t.Errorf("required lost: %v", s["required"])
	}
	// the upstream (Chat) must see lowercase types too
	out := string(buildChat(r, "m1", "api.example.com"))
	if strings.Contains(out, "OBJECT") || strings.Contains(out, "STRING") {
		t.Errorf("uppercase schema type reached upstream: %s", out)
	}
	if !strings.Contains(out, `"type":"object"`) {
		t.Errorf("lowercase schema missing from upstream body: %s", out)
	}
}

func TestGeminiThinkingBudgetMinusOneIsMedium(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	"generationConfig":{"maxOutputTokens":123,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.Effort != "medium" || !r.Thinking {
		t.Fatalf("effort=%q thinking=%v, want medium/true", r.Effort, r.Thinking)
	}
	if r.MaxTokens != 123 {
		t.Errorf("MaxTokens = %d, want 123", r.MaxTokens)
	}
	up := gemTestMap(t, buildChat(r, "m1", "api.example.com"))
	if up["reasoning_effort"] != "medium" {
		t.Errorf("upstream reasoning_effort = %v, want medium", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(123) {
		t.Errorf("upstream max_tokens = %v, want 123", up["max_tokens"])
	}
}

func TestGeminiThinkingBudgetAndLevelMapping(t *testing.T) {
	cases := []struct {
		cfg    string
		effort string
		think  bool
	}{
		{`{"thinkingBudget":0}`, "", false},
		{`{"thinkingBudget":0,"includeThoughts":true}`, "", true},
		{`{"thinkingBudget":24000}`, "high", true},
		{`{"thinkingLevel":"LOW"}`, "low", true},
		{`{"thinkingLevel":"HIGH"}`, "high", true},
	}
	for _, c := range cases {
		body := `{"contents":[],"generationConfig":{"thinkingConfig":` + c.cfg + `}}`
		r, err := parseGemini([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if r.Effort != c.effort || r.Thinking != c.think {
			t.Errorf("%s: effort=%q thinking=%v, want %q/%v", c.cfg, r.Effort, r.Thinking, c.effort, c.think)
		}
	}
}

func TestGeminiUsageExampleCounts(t *testing.T) {
	want := map[string]float64{"promptTokenCount": 10, "candidatesTokenCount": 3, "thoughtsTokenCount": 2, "totalTokenCount": 15}

	// through the Chat decoder, as a real upstream reply would arrive
	var col collector
	d := &chatDecoder{}
	chunk := `{"id":"x","model":"m1","choices":[{"delta":{"reasoning_content":"hm"},"finish_reason":null}]}`
	end := `{"id":"x","model":"m1","choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`
	for _, data := range []string{chunk, end} {
		if err := d.decode(data, col.add); err != nil {
			t.Fatal(err)
		}
	}
	res := col.finish()
	u := gemTestMap(t, renderGemini(res, "m1"))["usageMetadata"].(map[string]any)
	for k, v := range want {
		if u[k] != v {
			t.Errorf("chat path: %s = %v, want %v (usage %v)", k, u[k], v, u)
		}
	}

	// the IR directly
	u = gemTestMap(t, renderGemini(Result{Usage: Usage{Input: 10, Output: 5, Reasoning: 2}}, "m1"))["usageMetadata"].(map[string]any)
	for k, v := range want {
		if u[k] != v {
			t.Errorf("IR path: %s = %v, want %v", k, u[k], v)
		}
	}

	// cache hits count in the prompt and are reported apart
	u = gemTestMap(t, renderGemini(Result{Usage: Usage{Input: 4, CacheRead: 6, Output: 5, Reasoning: 2}}, "m1"))["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["cachedContentTokenCount"] != float64(6) {
		t.Errorf("cache usage = %v", u)
	}
}

func TestGeminiFinishReasonMapping(t *testing.T) {
	cases := map[string]string{"stop": "STOP", "tool": "STOP", "length": "MAX_TOKENS", "filter": "SAFETY", "": "STOP"}
	for in, want := range cases {
		if got := geminiFinish(in); got != want {
			t.Errorf("geminiFinish(%q) = %q, want %q", in, got, want)
		}
		m := gemTestMap(t, renderGemini(Result{Stop: in}, "m1"))
		if got := gemTestCand(t, m)["finishReason"]; got != want {
			t.Errorf("render stop %q: finishReason = %v, want %s", in, got, want)
		}
	}
	if stopFromChat("content_filter") != "filter" || geminiFinish(stopFromChat("content_filter")) != "SAFETY" {
		t.Error("content_filter must reach SAFETY")
	}
}

func TestGeminiFunctionResultMatchesByName(t *testing.T) {
	body := `{"contents":[
	{"role":"user","parts":[{"text":"weather?"}]},
	{"role":"model","parts":[{"functionCall":{"name":"f","args":{"n":1}}}]},
	{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"a"}}}]},
	{"role":"model","parts":[{"functionCall":{"name":"f","args":{"n":2}}}]},
	{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"b"}}}]},
	{"role":"model","parts":[{"functionCall":{"id":"x1","name":"g","args":{}}}]},
	{"role":"user","parts":[{"functionResponse":{"id":"x1","name":"g","response":{"temp":3}}}]}
	]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var calls, results []Part
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			switch p.Kind {
			case ToolCall:
				calls = append(calls, p)
			case ToolResult:
				results = append(results, p)
			}
		}
	}
	if len(calls) != 3 || len(results) != 3 {
		t.Fatalf("calls=%d results=%d, want 3/3", len(calls), len(results))
	}
	if calls[0].ID == "" || calls[0].ID == calls[1].ID {
		t.Errorf("calls without ids need distinct generated ids: %q %q", calls[0].ID, calls[1].ID)
	}
	if results[0].CallID != calls[0].ID || results[0].Text != "a" {
		t.Errorf("first result = %+v, want call %q text a", results[0], calls[0].ID)
	}
	if results[1].CallID != calls[1].ID || results[1].Text != "b" {
		t.Errorf("second result = %+v, want the most recent f call %q", results[1], calls[1].ID)
	}
	if calls[2].ID != "x1" || results[2].CallID != "x1" {
		t.Errorf("client ids must be kept: call %q result %q", calls[2].ID, results[2].CallID)
	}
	if results[2].Text != `{"temp":3}` {
		t.Errorf("non-output response should pass as JSON, got %q", results[2].Text)
	}
	// the upstream must see the same ids on both sides
	if out := string(buildChat(r, "m1", "api.example.com")); !strings.Contains(out, calls[1].ID) {
		t.Errorf("upstream lost call id %q: %s", calls[1].ID, out)
	}
}

func TestGeminiInlineImageAndAttachment(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[
	{"inlineData":{"mimeType":"image/png","data":"AAA"}},
	{"inlineData":{"mimeType":"application/pdf","data":"BBB"}},
	{"text":"look"}]}]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ps := r.Messages[0].Parts
	if len(ps) != 3 {
		t.Fatalf("parts = %d, want 3", len(ps))
	}
	if ps[0].Kind != Image || ps[0].MediaType != "image/png" || ps[0].Data != "AAA" {
		t.Errorf("image part = %+v", ps[0])
	}
	if ps[1].Kind != Text || !strings.Contains(ps[1].Text, "application/pdf") || strings.Contains(ps[1].Text, "BBB") {
		t.Errorf("attachment must become placeholder text without its data: %+v", ps[1])
	}
	if ps[2].Kind != Text || ps[2].Text != "look" {
		t.Errorf("text part = %+v", ps[2])
	}
}

func TestGeminiSystemToolChoiceAndJSON(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"be terse"}]},
	"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},
	"generationConfig":{"responseMimeType":"application/json",
	"responseSchema":{"type":"OBJECT","properties":{"x":{"type":"NUMBER"}}}}}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.System, "be terse") {
		t.Errorf("system = %q, must start with the system instruction", r.System)
	}
	if !strings.Contains(r.System, "JSON") || !strings.Contains(r.System, `"type":"object"`) || strings.Contains(r.System, "OBJECT") {
		t.Errorf("JSON instruction with lowercase schema missing: %q", r.System)
	}
	if r.ToolChoice != "name:f" {
		t.Errorf("ToolChoice = %q, want name:f", r.ToolChoice)
	}

	for mode, want := range map[string]string{
		`{"mode":"NONE"}`:                                  "none",
		`{"mode":"AUTO"}`:                                  "auto",
		`{"mode":"ANY"}`:                                   "required",
		`{"mode":"ANY","allowedFunctionNames":["a","b"]}`:  "required",
	} {
		r, err := parseGemini([]byte(`{"contents":[],"toolConfig":{"functionCallingConfig":` + mode + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.ToolChoice != want {
			t.Errorf("%s → %q, want %q", mode, r.ToolChoice, want)
		}
	}
}

func TestGeminiThoughtsAndRoles(t *testing.T) {
	body := `{"contents":[
	{"role":"user","parts":[{"text":"q"}]},
	{"role":"model","parts":[{"text":"reasoning","thought":true},{"text":"answer"}]}]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 || r.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", r.Messages)
	}
	ps := r.Messages[1].Parts
	if len(ps) != 2 || ps[0].Kind != Thinking || ps[0].Text != "reasoning" || ps[1].Kind != Text {
		t.Errorf("model parts = %+v", ps)
	}
}

func TestGeminiRenderNonStream(t *testing.T) {
	res := Result{ID: "r1", Model: "m1", Stop: "tool", Parts: []Part{
		{Kind: Thinking, Text: "t"},
		{Kind: Text, Text: "answer"},
		{Kind: ToolCall, ID: "c1", Name: "f", Args: json.RawMessage(`{"a":1}`)},
	}}
	m := gemTestMap(t, renderGemini(res, "m1"))
	parts := gemTestParts(t, m)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3: %v", len(parts), parts)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "t" {
		t.Errorf("thought part = %v", parts[0])
	}
	if parts[1]["text"] != "answer" {
		t.Errorf("text part = %v", parts[1])
	}
	fc := parts[2]["functionCall"].(map[string]any)
	if fc["id"] != "c1" || fc["name"] != "f" || fc["args"].(map[string]any)["a"] != float64(1) {
		t.Errorf("functionCall = %v", fc)
	}
	if gemTestCand(t, m)["finishReason"] != "STOP" || m["responseId"] != "r1" || m["modelVersion"] != "m1" {
		t.Errorf("reply fields = %v", m)
	}
}

func TestGeminiStreamFunctionCallIsWhole(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &geminiEncoder{w: newSSEWriter(rec), model: "m1"}
	for _, ev := range []Event{
		{Kind: KStart, MsgID: "r1", Model: "m1"},
		{Kind: KThink, Text: "hmm"},
		{Kind: KText, Text: "Let me check."},
		{Kind: KToolStart, ID: "call_9", Name: "get_weather"},
		{Kind: KToolArgs, Text: `{"city":`},
		{Kind: KToolArgs, Text: `"Paris"}`},
		{Kind: KStop, Stop: "tool"},
		{Kind: KUsage, Usage: Usage{Input: 10, Output: 5, Reasoning: 2}},
	} {
		enc.event(ev)
	}
	enc.finish()

	evs := gemTestSSE(t, rec.Body.String())
	calls := 0
	var call map[string]any
	for _, ev := range evs {
		for _, p := range gemTestParts(t, ev) {
			if fc, ok := p["functionCall"].(map[string]any); ok {
				calls++
				call = fc
				if gemTestCand(t, ev)["finishReason"] != nil {
					t.Errorf("call must not ride on a finish event")
				}
			}
		}
	}
	if calls != 1 {
		t.Fatalf("functionCall events = %d, want exactly 1 whole call\n%s", calls, rec.Body.String())
	}
	if call["id"] != "call_9" || call["name"] != "get_weather" || call["args"].(map[string]any)["city"] != "Paris" {
		t.Errorf("call = %v", call)
	}
	last := evs[len(evs)-1]
	if gemTestCand(t, last)["finishReason"] != "STOP" {
		t.Errorf("last event finishReason = %v, want STOP", gemTestCand(t, last)["finishReason"])
	}
	u := last["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("last usage = %v", u)
	}
}

func TestGeminiErrorShape(t *testing.T) {
	e := gemTestMap(t, geminiErrorBody(404, "unknown model x"))["error"].(map[string]any)
	if e["code"] != float64(404) || e["status"] != "NOT_FOUND" || e["message"] != "unknown model x" {
		t.Errorf("error = %v", e)
	}
	e = gemTestMap(t, geminiErrorBody(400, "bad"))["error"].(map[string]any)
	if e["status"] != "INVALID_ARGUMENT" {
		t.Errorf("400 status = %v", e["status"])
	}
	if _, err := parseGemini([]byte("{")); err == nil {
		t.Error("malformed body must fail to parse")
	}
}
