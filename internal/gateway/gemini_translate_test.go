package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// gtrParse reads a Gemini request body for model m1, as the route will.
func gtrParse(t *testing.T, body string) *Request {
	t.Helper()
	r, err := parseGemini("m1", false, []byte(body))
	if err != nil {
		t.Fatalf("parseGemini: %v", err)
	}
	return r
}

// gtrObj decodes a JSON object.
func gtrObj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

// gtrArr is the array under key k of m.
func gtrArr(t *testing.T, m map[string]any, k string) []any {
	t.Helper()
	a, ok := m[k].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %v", k, m)
	}
	return a
}

// gtrMap is element i of a as an object.
func gtrMap(t *testing.T, a []any, i int) map[string]any {
	t.Helper()
	if i >= len(a) {
		t.Fatalf("index %d out of %d", i, len(a))
	}
	m, ok := a[i].(map[string]any)
	if !ok {
		t.Fatalf("element %d is not an object: %v", i, a[i])
	}
	return m
}

// gtrSSE returns the JSON payload of each data line of a stream.
func gtrSSE(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			out = append(out, gtrObj(t, []byte(strings.TrimPrefix(line, "data: "))))
		}
	}
	return out
}

const gtrToolBody = `{
 "systemInstruction": {"parts":[{"text":"be brief"}]},
 "contents":[
  {"role":"user","parts":[{"text":"weather?"}]},
  {"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
  {"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"output":"sunny"}}}]}
 ],
 "tools":[{"functionDeclarations":[{"name":"get_weather","description":"w",
   "parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}}]}],
 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
 "generationConfig":{"maxOutputTokens":77,"thinkingConfig":{"thinkingBudget":-1}}
}`

func TestGtrChatTranslation(t *testing.T) {
	r := gtrParse(t, gtrToolBody)
	m := gtrObj(t, buildChat(r, "up/m", "api.example.com"))

	msgs := gtrArr(t, m, "messages")
	sys := gtrMap(t, msgs, 0)
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Fatalf("system message wrong: %v", sys)
	}
	asst := gtrMap(t, msgs, 2)
	calls := gtrArr(t, asst, "tool_calls")
	callID := gtrMap(t, calls, 0)["id"].(string)
	if callID == "" {
		t.Fatal("generated call id is empty")
	}
	tool := gtrMap(t, msgs, 3)
	if tool["role"] != "tool" || tool["tool_call_id"] != callID || tool["content"] != "sunny" {
		t.Fatalf("tool message does not pair with call %q: %v", callID, tool)
	}
	fn := gtrMap(t, gtrArr(t, m, "tools"), 0)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("schema type not lowered: %v", params)
	}
	city := params["properties"].(map[string]any)["city"].(map[string]any)
	if city["type"] != "string" {
		t.Fatalf("nested schema type not lowered: %v", city)
	}
	choice := m["tool_choice"].(map[string]any)["function"].(map[string]any)
	if choice["name"] != "get_weather" {
		t.Fatalf("ANY with one function should name it: %v", m["tool_choice"])
	}
	if m["reasoning_effort"] != "medium" {
		t.Fatalf("thinkingBudget -1 should be medium, got %v", m["reasoning_effort"])
	}
	if m["max_tokens"] != float64(77) {
		t.Fatalf("maxOutputTokens not mapped: %v", m["max_tokens"])
	}
}

func TestGtrCallIDsPairByName(t *testing.T) {
	body := `{"contents":[
	 {"role":"model","parts":[{"functionCall":{"name":"f","args":{"n":1}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"a"}}}]},
	 {"role":"model","parts":[{"functionCall":{"name":"f","args":{"n":2}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"b"}}}]}]}`
	r := gtrParse(t, body)
	var ids []string
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall {
				ids = append(ids, p.ID)
			}
			if p.Kind == ToolResult {
				ids = append(ids, "result:"+p.CallID)
			}
		}
	}
	if len(ids) != 4 || ids[0] == ids[2] || ids[1] != "result:"+ids[0] || ids[3] != "result:"+ids[2] {
		t.Fatalf("pairing wrong: %v", ids)
	}
}

func TestGtrExplicitIDsKept(t *testing.T) {
	body := `{"contents":[
	 {"role":"model","parts":[{"functionCall":{"id":"up-1","name":"f","args":{}}}]},
	 {"role":"user","parts":[{"functionResponse":{"id":"up-1","name":"f","response":{"ok":true}}}]}]}`
	r := gtrParse(t, body)
	if r.Messages[0].Parts[0].ID != "up-1" || r.Messages[1].Parts[0].CallID != "up-1" {
		t.Fatalf("explicit id not kept: %+v", r.Messages)
	}
	if r.Messages[1].Parts[0].Text != `{"ok":true}` {
		t.Fatalf("response text: %q", r.Messages[1].Parts[0].Text)
	}
}

func TestGtrResponsesTranslation(t *testing.T) {
	r := gtrParse(t, gtrToolBody)
	m := gtrObj(t, buildResponses(r, "up/m"))
	if m["instructions"] != "be brief" {
		t.Fatalf("instructions: %v", m["instructions"])
	}
	var callID, outID string
	for _, it := range gtrArr(t, m, "input") {
		item := it.(map[string]any)
		switch item["type"] {
		case "function_call":
			callID = item["call_id"].(string)
		case "function_call_output":
			outID = item["call_id"].(string)
		}
	}
	if callID == "" || callID != outID {
		t.Fatalf("function_call and output not paired: %q %q", callID, outID)
	}
}

func TestGtrAnthropicTranslation(t *testing.T) {
	r := gtrParse(t, gtrToolBody)
	m := gtrObj(t, buildAnthropic(r, "up/m"))
	if m["system"] != "be brief" {
		t.Fatalf("system field: %v", m["system"])
	}
	// thinking is on (budget -1 → medium), so the upstream raises max_tokens
	// above the requested 77 to leave room for the reasoning budget.
	if mt, ok := m["max_tokens"].(float64); !ok || mt < 77 {
		t.Fatalf("max_tokens: %v", m["max_tokens"])
	}
}

func TestGtrThinkingBudgets(t *testing.T) {
	cases := map[string]string{"-1": "medium", "0": "low", "3000": "low", "5000": "medium", "30000": "high"}
	for budget, want := range cases {
		body := `{"contents":[{"role":"user","parts":[{"text":"x"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":` + budget + `}}}`
		if got := gtrParse(t, body).Effort; got != want {
			t.Errorf("budget %s: effort %q, want %q", budget, got, want)
		}
	}
}

func TestGtrToolChoices(t *testing.T) {
	cases := map[string]string{
		`"mode":"NONE"`: "none",
		`"mode":"AUTO"`: "auto",
		`"mode":"ANY","allowedFunctionNames":["a","b"]`: "required",
	}
	for cfg, want := range cases {
		body := `{"contents":[],"toolConfig":{"functionCallingConfig":{` + cfg + `}}}`
		if got := gtrParse(t, body).ToolChoice; got != want {
			t.Errorf("%s: choice %q, want %q", cfg, got, want)
		}
	}
}

func TestGtrJSONMode(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"x"}]}],"generationConfig":{
	 "responseMimeType":"application/json",
	 "responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`
	r := gtrParse(t, body)
	if !strings.Contains(r.System, "JSON") || !strings.Contains(r.System, `"type":"integer"`) {
		t.Fatalf("system lacks schema instruction: %q", r.System)
	}
	m := gtrObj(t, buildChat(r, "up/m", "api.example.com"))
	if _, ok := m["response_format"]; ok {
		t.Fatal("JSON mode must not set a response_format upstream")
	}
}

func TestGtrImagesAndAttachments(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[
	 {"text":"look"},
	 {"inlineData":{"mimeType":"image/png","data":"QUJD"}},
	 {"inlineData":{"mimeType":"application/pdf","data":"AAAA"}},
	 {"fileData":{"mimeType":"image/jpeg","fileUri":"https://x.test/a.jpg"}}]}]}`
	r := gtrParse(t, body)
	m := gtrObj(t, buildChat(r, "up/m", "api.example.com"))
	content := gtrArr(t, gtrMap(t, gtrArr(t, m, "messages"), 0), "content")
	if got := gtrMap(t, content, 1)["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,QUJD" {
		t.Fatalf("inline image url: %v", got)
	}
	if got := gtrMap(t, content, 2)["text"].(string); !strings.Contains(got, "application/pdf") {
		t.Fatalf("pdf placeholder: %q", got)
	}
	if got := gtrMap(t, content, 3)["image_url"].(map[string]any)["url"]; got != "https://x.test/a.jpg" {
		t.Fatalf("file image url: %v", got)
	}
}

func TestGtrRenderCompact(t *testing.T) {
	res := Result{ID: "chatcmpl-1", Model: "m1", Stop: "tool", Usage: Usage{Input: 4, Output: 6, Reasoning: 2},
		Parts: []Part{{Kind: Thinking, Text: "hm"}, {Kind: Text, Text: "hi"},
			{Kind: ToolCall, ID: "c1", Name: "f", Args: json.RawMessage(`{"a":1}`)}}}
	s := string(renderGemini(res, "x"))
	if !strings.Contains(s, `"finishReason":"STOP"`) {
		t.Fatalf("not compact or wrong finish: %s", s)
	}
	m := gtrObj(t, []byte(s))
	parts := gtrArr(t, gtrMap(t, gtrArr(t, m, "candidates"), 0)["content"].(map[string]any), "parts")
	if gtrMap(t, parts, 0)["thought"] != true {
		t.Fatalf("thought part: %v", parts[0])
	}
	fc := gtrMap(t, parts, 2)["functionCall"].(map[string]any)
	if fc["id"] != "c1" || fc["name"] != "f" {
		t.Fatalf("function call: %v", fc)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["thoughtsTokenCount"] != float64(2) || u["promptTokenCount"] != float64(4) || u["candidatesTokenCount"] != float64(6) {
		t.Fatalf("usage: %v", u)
	}
	if m["modelVersion"] != "m1" || m["responseId"] != "chatcmpl-1" {
		t.Fatalf("model/id: %v %v", m["modelVersion"], m["responseId"])
	}
}

func TestGtrStreamOrder(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &geminiEncoder{w: newSSEWriter(rec), model: "m1"}
	enc.event(Event{Kind: KStart, MsgID: "r1", Model: "up-model"})
	enc.event(Event{Kind: KThink, Text: "a"})
	enc.event(Event{Kind: KText, Text: "b"})
	enc.event(Event{Kind: KToolStart, ID: "t1", Name: "f"})
	enc.event(Event{Kind: KToolArgs, Text: `{"x":`})
	enc.event(Event{Kind: KToolArgs, Text: `1}`})
	enc.event(Event{Kind: KStop, Stop: "tool"})
	enc.event(Event{Kind: KUsage, Usage: Usage{Input: 10, Output: 5, Reasoning: 3}})
	enc.finish()

	evs := gtrSSE(t, rec.Body.String())
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d: %s", len(evs), rec.Body.String())
	}
	p0 := gtrMap(t, gtrArr(t, gtrMap(t, gtrArr(t, evs[0], "candidates"), 0)["content"].(map[string]any), "parts"), 0)
	if p0["text"] != "a" || p0["thought"] != true {
		t.Fatalf("first event should be the thought: %v", p0)
	}
	p1 := gtrMap(t, gtrArr(t, gtrMap(t, gtrArr(t, evs[1], "candidates"), 0)["content"].(map[string]any), "parts"), 0)
	if p1["text"] != "b" {
		t.Fatalf("second event should be the text: %v", p1)
	}
	last := gtrMap(t, gtrArr(t, evs[2], "candidates"), 0)
	fc := gtrMap(t, gtrArr(t, last["content"].(map[string]any), "parts"), 0)["functionCall"].(map[string]any)
	if fc["id"] != "t1" || fc["args"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("function call id or args: %v", fc)
	}
	if last["finishReason"] != "STOP" {
		t.Fatalf("finishReason on last event: %v", last)
	}
	u := evs[2]["usageMetadata"].(map[string]any)
	if u["thoughtsTokenCount"] != float64(3) || u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(5) {
		t.Fatalf("usage on last event: %v", u)
	}
}

func TestGtrStreamEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &geminiEncoder{w: newSSEWriter(rec), model: "m1"}
	enc.finish()
	evs := gtrSSE(t, rec.Body.String())
	if len(evs) != 1 {
		t.Fatalf("want one final event, got %d", len(evs))
	}
	cand := gtrMap(t, gtrArr(t, evs[0], "candidates"), 0)
	if cand["finishReason"] != "STOP" {
		t.Fatalf("finishReason: %v", cand)
	}
}

func TestGtrCountBodyAndError(t *testing.T) {
	wrapped := []byte(`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	if r, err := parseGemini("m1", false, geminiCountBody(wrapped)); err != nil || len(r.Messages) != 1 {
		t.Fatalf("wrapped count body: %v %+v", err, r)
	}
	if r, err := parseGemini("m1", false, geminiCountBody([]byte(`{"contents":[]}`))); err != nil || len(r.Messages) != 0 {
		t.Fatalf("bare count body: %v %+v", err, r)
	}
	e := string(renderGeminiError(404, "NOT_FOUND", "no such model"))
	if !strings.Contains(e, `"status":"NOT_FOUND"`) {
		t.Fatalf("error body: %s", e)
	}
}
