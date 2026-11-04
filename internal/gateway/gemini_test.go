package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiBody is a request with a system instruction, a function call and its
// result in the history, a tool declaration with upper-case types, and a
// thinking budget of -1.
const geminiBody = `{
 "systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[
  {"role":"user","parts":[{"text":"read a.txt"}]},
  {"role":"model","parts":[{"functionCall":{"id":"c1","name":"read","args":{"path":"a.txt"}}}]},
  {"role":"user","parts":[{"functionResponse":{"id":"c1","name":"read","response":{"output":"hello"}}}]}
 ],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
   "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
 "generationConfig":{"maxOutputTokens":321,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}
}`

// chatReply is what an OpenAI Chat upstream streams: reasoning, text, one
// tool call split over two fragments, then usage with 2 reasoning tokens
// inside the 5 completion tokens.
var chatReply = sse(
	`data: {"id":"up1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
	`data: {"choices":[{"delta":{"content":"Let me read."}}]}`,
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"up_1","type":"function","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.txt\"}"}}]}}]}`,
	`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`,
)

// geminiChunks reads the data lines of an SSE body as JSON objects.
func geminiChunks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
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

// candParts is the parts of a Gemini response's first candidate.
func candParts(m map[string]any) []map[string]any {
	cands, _ := m["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	c, _ := cands[0].(map[string]any)
	content, _ := c["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		if pm, ok := p.(map[string]any); ok {
			out = append(out, pm)
		}
	}
	return out
}

func finishOf(m map[string]any) string {
	cands, _ := m["candidates"].([]any)
	if len(cands) == 0 {
		return ""
	}
	c, _ := cands[0].(map[string]any)
	s, _ := c["finishReason"].(string)
	return s
}

// checkUsage asserts the usage numbers of the example: prompt 10, completion
// 5 of which 2 reasoning, so candidates 3 and thoughts 2.
func checkUsage(t *testing.T, u map[string]any) {
	t.Helper()
	want := map[string]float64{"promptTokenCount": 10, "candidatesTokenCount": 3, "thoughtsTokenCount": 2}
	for k, v := range want {
		if got, _ := u[k].(float64); got != v {
			t.Errorf("%s = %v, want %v (%v)", k, u[k], v, u)
		}
	}
}

func TestGeminiStreamTranslatesChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	chunks := geminiChunks(t, body)
	var thought, text, calls int
	for _, c := range chunks {
		for _, p := range candParts(c) {
			switch {
			case p["thought"] == true && p["text"] == "hmm":
				thought++
			case p["text"] == "Let me read.":
				text++
			case p["functionCall"] != nil:
				calls++
				fc := p["functionCall"].(map[string]any)
				args := fc["args"].(map[string]any)
				if fc["id"] != "up_1" || fc["name"] != "read" || args["path"] != "a.txt" {
					t.Errorf("function call %v", fc)
				}
			}
		}
	}
	if thought != 1 || text != 1 || calls != 1 {
		t.Errorf("thought %d text %d calls %d in %s", thought, text, calls, body)
	}
	last := chunks[len(chunks)-1]
	if finishOf(last) != "STOP" {
		t.Errorf("last finishReason %q", finishOf(last))
	}
	u, _ := last["usageMetadata"].(map[string]any)
	checkUsage(t, u)

	// what the upstream was sent
	got := string(f.got)
	for _, want := range []string{`"reasoning_effort":"medium"`, `"max_tokens":321`, `"type":"object"`,
		`"type":"string"`, `"tool_call_id":"c1"`, `"tool_calls"`, `be brief`} {
		if !strings.Contains(got, want) {
			t.Errorf("upstream body lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"OBJECT"`) || strings.Contains(got, `"STRING"`) {
		t.Errorf("upper-case schema types reached upstream: %s", got)
	}
}

func TestGeminiGenerateContentIsWholeReply(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: chatReply})
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if finishOf(m) != "STOP" {
		t.Errorf("finishReason %q in %s", finishOf(m), body)
	}
	var sawCall, sawThought bool
	for _, p := range candParts(m) {
		if fc, ok := p["functionCall"].(map[string]any); ok {
			sawCall = fc["id"] == "up_1" && fc["name"] == "read"
		}
		if p["thought"] == true {
			sawThought = true
		}
	}
	if !sawCall || !sawThought {
		t.Errorf("parts %s", body)
	}
	u, _ := m["usageMetadata"].(map[string]any)
	checkUsage(t, u)
	if m["modelVersion"] == nil || m["responseId"] == nil {
		t.Errorf("missing modelVersion/responseId: %s", body)
	}
}

func TestGeminiUnknownModelIsNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", geminiBody)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, body := range []string{
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there world"}]}]}}`,
		`{"contents":[{"role":"user","parts":[{"text":"hello there world"}]}]}`,
	} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		var m map[string]any
		json.Unmarshal([]byte(out), &m)
		if code != 200 || m["totalTokens"] == nil {
			t.Errorf("%d %s", code, out)
		}
		if n, _ := m["totalTokens"].(float64); n <= 0 {
			t.Errorf("totalTokens %v", m["totalTokens"])
		}
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) ||
		!strings.Contains(rec.Body.String(), `"displayName"`) || !strings.Contains(rec.Body.String(), `"streamGenerateContent"`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiInfoAndNotFoundListTheAPI(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "/v1beta/models/{model}:generateContent") {
		t.Errorf("info %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "/v1beta/models") {
		t.Errorf("404 %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiToolChoiceAndJSONMode(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}],
	 "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}},{"name":"write","parameters":{"type":"OBJECT"}}]}],
	 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	 "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"ok":{"type":"BOOLEAN"}}}}}`
	if code, out := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	got := string(f.got)
	// the schema travels inside the system message, so its quotes are escaped
	for _, want := range []string{`"tool_choice":{"function":{"name":"read"},"type":"function"}`, `JSON Schema`, `boolean`} {
		if !strings.Contains(got, want) {
			t.Errorf("upstream lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "BOOLEAN") || strings.Contains(got, "OBJECT") {
		t.Errorf("upper-case schema types reached upstream: %s", got)
	}
	if strings.Contains(got, `"write"`) {
		t.Errorf("disallowed tool reached upstream: %s", got)
	}
}

func TestGeminiFunctionResultMatchesByName(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	body := `{"contents":[
	 {"role":"user","parts":[{"text":"go"}]},
	 {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}}]}]}`
	if code, out := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var up struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatal(err)
	}
	var callID, resultID string
	for _, m := range up.Messages {
		if len(m.ToolCalls) > 0 {
			callID = m.ToolCalls[0].ID
		}
		if m.Role == "tool" {
			resultID = m.ToolCallID
		}
	}
	if callID == "" || callID != resultID {
		t.Errorf("call id %q, result id %q", callID, resultID)
	}
}

func TestGeminiAttachments(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	body := `{"contents":[{"role":"user","parts":[
	 {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	 {"inlineData":{"mimeType":"application/pdf","data":"BBBB"}}]}]}`
	if code, out := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	got := string(f.got)
	if !strings.Contains(got, `data:image/png;base64,AAAA`) || !strings.Contains(got, `[attachment omitted: application/pdf]`) {
		t.Errorf("attachments %s", got)
	}
}

func TestGeminiStreamErrorsAreGeminiShaped(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"no balance"}}`})
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
	if code != 402 || !strings.Contains(body, `"error":{"code":402`) || !strings.Contains(body, "no balance") {
		t.Errorf("%d %s", code, body)
	}
}
