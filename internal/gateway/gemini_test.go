package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiReq has a system instruction, a function call and its result in the
// history, uppercase tool schema types, and thinking and token settings.
const geminiReq = `{
  "systemInstruction": {"parts": [{"text": "be brief"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "read a.go"}]},
    {"role": "model", "parts": [{"functionCall": {"name": "read", "args": {"path": "x"}}}]},
    {"role": "user", "parts": [{"functionResponse": {"name": "read", "response": {"output": "package x"}}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "read", "description": "read a file",
    "parameters": {"type": "OBJECT", "properties": {"path": {"type": "STRING"}}, "required": ["path"]}}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "ANY"}},
  "generationConfig": {"maxOutputTokens": 100, "temperature": 0.5,
    "thinkingConfig": {"thinkingBudget": -1, "includeThoughts": true}}
}`

// geminiUpstream is an OpenAI Chat upstream that thinks, talks, calls a tool
// in fragments, and reports usage with 2 reasoning tokens.
func geminiUpstream(t *testing.T) *fake {
	return &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
}

// checkChatUpstream checks what the Chat upstream was sent for geminiReq.
func checkChatUpstream(t *testing.T, f *fake) {
	t.Helper()
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages: %v", msgs)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] == "" || msgs[3].(map[string]any)["tool_call_id"] != call["id"] {
		t.Errorf("result not matched to its call: %v", msgs[2:])
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lowercased: %v", params)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) || up["tool_choice"] != "required" {
		t.Errorf("upstream params: %s", f.got)
	}
}

// flatParts gathers the parts of every chunk, in order.
func flatParts(evs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		for _, c := range e["candidates"].([]any) {
			content := c.(map[string]any)["content"].(map[string]any)
			for _, p := range content["parts"].([]any) {
				out = append(out, p.(map[string]any))
			}
		}
	}
	return out
}

// checkUsage checks the usageMetadata for upstream usage of 10 prompt, 5
// completion, 2 reasoning.
func checkUsage(t *testing.T, v any) {
	t.Helper()
	m, _ := v.(map[string]any)
	if m["promptTokenCount"] != float64(10) || m["candidatesTokenCount"] != float64(3) || m["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usageMetadata: %v", m)
	}
}

func TestGeminiStreamToChatUpstream(t *testing.T) {
	f := geminiUpstream(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	checkChatUpstream(t, f)
	evs := events(body)
	parts := flatParts(evs)
	if len(parts) != 4 {
		t.Fatalf("parts: %d in %s", len(parts), body)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "hmm" {
		t.Errorf("thought part: %v", parts[0])
	}
	if parts[1]["text"] != "Hi " || parts[2]["text"] != "there" {
		t.Errorf("text parts: %v %v", parts[1], parts[2])
	}
	fc, _ := parts[3]["functionCall"].(map[string]any)
	args, _ := fc["args"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("functionCall: %v", parts[3])
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", cand)
	}
	checkUsage(t, last["usageMetadata"])
}

func TestGeminiGenerateContent(t *testing.T) {
	f := geminiUpstream(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	checkChatUpstream(t, f)
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	cand := out["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", cand)
	}
	var fc map[string]any
	for _, p := range cand["content"].(map[string]any)["parts"].([]any) {
		if m, ok := p.(map[string]any)["functionCall"].(map[string]any); ok {
			fc = m
		}
	}
	if fc == nil || fc["id"] != "call_1" || fc["name"] != "read" {
		t.Fatalf("functionCall missing: %s", body)
	}
	checkUsage(t, out["usageMetadata"])
}

func TestGeminiCountModelsAndErrors(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, b := range []string{geminiReq, `{"generateContentRequest":` + geminiReq + `}`} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		if n, _ := out["totalTokens"].(float64); code != 200 || n <= 0 {
			t.Errorf("countTokens %d: %s", code, body)
		}
	}

	code, body := post(t, "/v1beta/models/nope:generateContent", geminiReq)
	var e struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(body), &e)
	if code != 404 || e.Error.Code != 404 || e.Error.Status != "NOT_FOUND" || e.Error.Message == "" {
		t.Errorf("unknown model: %d %s", code, body)
	}

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var list struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	found := false
	for _, m := range list.Models {
		if m.Name == "models/fake/m1" && len(m.Methods) == 3 {
			found = true
		}
	}
	if rec.Code != 200 || !found {
		t.Errorf("model list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiUsageMapping(t *testing.T) {
	got := geminiUsage(Usage{Input: 10, Output: 5, Reasoning: 2})
	if got["promptTokenCount"] != 10 || got["candidatesTokenCount"] != 3 || got["thoughtsTokenCount"] != 2 {
		t.Errorf("usage: %v", got)
	}
	cached := geminiUsage(Usage{Input: 4, CacheRead: 6, Output: 5, Reasoning: 2})
	if cached["promptTokenCount"] != 10 || cached["cachedContentTokenCount"] != 6 {
		t.Errorf("cached usage: %v", cached)
	}
}

func TestGeminiSchemaThinkingAndFinish(t *testing.T) {
	s := string(lowerSchema(json.RawMessage(`{"type":"OBJECT","properties":{"type":{"type":"STRING"},"items":{"type":"ARRAY","items":{"type":"INTEGER"}}}}`)))
	for _, want := range []string{`"type":"object"`, `"type":"array"`, `"type":"integer"`, `"type":"string"`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema %s lacks %s", s, want)
		}
	}
	for _, c := range []struct{ cfg, want string }{
		{`{"thinkingBudget":-1}`, "medium"},
		{`{"thinkingBudget":0}`, ""},
		{`{"thinkingLevel":"HIGH","includeThoughts":true}`, "high"},
		{`{"thinkingBudget":2000}`, "low"},
	} {
		r, err := parseGemini([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":` + c.cfg + `}}`))
		if err != nil || r.Effort != c.want {
			t.Errorf("thinking %s: effort %q, want %q (%v)", c.cfg, r.Effort, c.want, err)
		}
	}
	r, err := parseGemini([]byte(`{"contents":[],"generationConfig":{"responseMimeType":"application/json",
	  "responseSchema":{"type":"OBJECT"}}}`))
	if err != nil || !strings.Contains(r.System, "JSON") || !strings.Contains(r.System, `"type":"object"`) {
		t.Errorf("json mode: %q (%v)", r.System, err)
	}
	if geminiFinish("length") != "MAX_TOKENS" || geminiFinish("filter") != "SAFETY" || geminiFinish("stop") != "STOP" {
		t.Errorf("finish reasons")
	}
	if geminiStatus(404) != "NOT_FOUND" || geminiStatus(400) != "INVALID_ARGUMENT" || geminiStatus(500) != "INTERNAL" {
		t.Errorf("status names")
	}
}

func TestGeminiParseHistory(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	 {"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]},
	 {"role":"model","parts":[{"functionCall":{"name":"read","args":{"p":1}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}}]},
	 {"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"application/pdf","data":"AA"}}]}
	]}`))
	if err != nil || len(r.Messages) != 4 {
		t.Fatalf("messages: %v", err)
	}
	if r.Messages[0].Parts[0].ID != "gcall_1" || r.Messages[1].Parts[0].ID != "gcall_2" {
		t.Errorf("call ids: %v %v", r.Messages[0].Parts[0].ID, r.Messages[1].Parts[0].ID)
	}
	res := r.Messages[2].Parts[0]
	if res.Kind != ToolResult || res.CallID != "gcall_2" || res.Text != "ok" {
		t.Errorf("result matched by name: %+v", res)
	}
	if img := r.Messages[3].Parts[0]; img.Kind != Image || img.MediaType != "image/png" {
		t.Errorf("image part: %+v", img)
	}
	if pdf := r.Messages[3].Parts[1]; pdf.Kind != Text || !strings.Contains(pdf.Text, "application/pdf") {
		t.Errorf("attachment placeholder: %+v", pdf)
	}
}
