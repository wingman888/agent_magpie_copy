package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemHTTPMap decodes a JSON object for inspection.
func gemHTTPMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

// gemHTTPEvents reads the data lines of a Gemini SSE body.
func gemHTTPEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if !strings.HasPrefix(block, "data: ") {
			continue
		}
		out = append(out, gemHTTPMap(t, []byte(strings.TrimPrefix(block, "data: "))))
	}
	return out
}

// gemHTTPParts lists the parts of every candidate in the events, in order.
func gemHTTPParts(evs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, ev := range evs {
		cands, _ := ev["candidates"].([]any)
		for _, c := range cands {
			cm, _ := c.(map[string]any)
			content, _ := cm["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					out = append(out, pm)
				}
			}
		}
	}
	return out
}

// gemHTTPBody is a Gemini request with a system instruction, a function
// call and its result in the history, a tool declaration with uppercase
// schema types, and thinking and output limits.
const gemHTTPBody = `{
  "systemInstruction": {"parts": [{"text": "be brief"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "read a.go"}]},
    {"role": "model", "parts": [{"functionCall": {"id": "t0", "name": "read", "args": {"path": "x"}}}]},
    {"role": "user", "parts": [{"functionResponse": {"id": "t0", "name": "read", "response": {"output": "package x"}}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "read", "description": "read a file",
    "parameters": {"type": "OBJECT", "properties": {"path": {"type": "STRING"}}, "required": ["path"]}}]}],
  "generationConfig": {"maxOutputTokens": 100, "thinkingConfig": {"thinkingBudget": -1, "includeThoughts": true}}
}`

// gemHTTPChatReply is an OpenAI Chat stream: reasoning, text, a tool call
// split over several chunks, and usage with reasoning tokens.
var gemHTTPChatReply = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`)

func TestGeminiHTTPStream(t *testing.T) {
	f := &fake{t: t, reply: gemHTTPChatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", gemHTTPBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := gemHTTPEvents(t, body)
	parts := gemHTTPParts(evs)
	var thought, call bool
	var text strings.Builder
	for _, p := range parts {
		if p["thought"] == true && p["text"] == "hmm" {
			thought = true
		}
		if p["thought"] != true {
			if s, ok := p["text"].(string); ok {
				text.WriteString(s)
			}
		}
		if fc, ok := p["functionCall"].(map[string]any); ok {
			args, _ := fc["args"].(map[string]any)
			if fc["id"] == "call_1" && fc["name"] == "read" && args["path"] == "a.go" {
				call = true
			}
		}
	}
	if !thought || text.String() != "Hi there" || !call {
		t.Errorf("thought=%v text=%q call=%v in %s", thought, text.String(), call, body)
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" || last["usageMetadata"] == nil {
		t.Errorf("last event: %v", last)
	}
	if !strings.Contains(string(f.got), `"role":"tool"`) || !strings.Contains(string(f.got), `"tool_call_id":"t0"`) {
		t.Errorf("upstream lost the function result: %s", f.got)
	}
}

func TestGeminiHTTPGenerate(t *testing.T) {
	f := &fake{t: t, reply: gemHTTPChatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", gemHTTPBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := gemHTTPMap(t, []byte(body))
	cand := m["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", cand["finishReason"])
	}
	found := false
	for _, p := range gemHTTPParts(gemHTTPEvents2(t, body)) {
		if fc, ok := p["functionCall"].(map[string]any); ok && fc["id"] == "call_1" && fc["name"] == "read" {
			found = true
		}
	}
	if !found {
		t.Errorf("no functionCall in %s", body)
	}
}

// gemHTTPEvents2 wraps a single non-streamed reply as one event, so the
// parts helper can read it.
func gemHTTPEvents2(t *testing.T, body string) []map[string]any {
	t.Helper()
	return []map[string]any{gemHTTPMap(t, []byte(body))}
}

func TestGeminiHTTPUpstreamRequest(t *testing.T) {
	f := &fake{t: t, reply: gemHTTPChatReply}
	setup(t, provider.Chat, f)
	if code, body := post(t, "/v1beta/models/fake/m1:generateContent", gemHTTPBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	got := string(f.got)
	if strings.Contains(got, `"OBJECT"`) || strings.Contains(got, `"STRING"`) || !strings.Contains(got, `"type":"object"`) {
		t.Errorf("schema types not lowercased: %s", got)
	}
	if !strings.Contains(got, `"reasoning_effort":"medium"`) {
		t.Errorf("thinkingBudget -1 should be medium effort: %s", got)
	}
	if !strings.Contains(got, `"max_tokens":100`) && !strings.Contains(got, `"max_completion_tokens":100`) {
		t.Errorf("maxOutputTokens not passed: %s", got)
	}
	if !strings.Contains(got, `"model":"m1"`) {
		t.Errorf("upstream model: %s", got)
	}
}

func TestGeminiHTTPUsage(t *testing.T) {
	f := &fake{t: t, reply: gemHTTPChatReply}
	setup(t, provider.Chat, f)
	_, body := post(t, "/v1beta/models/fake/m1:generateContent", gemHTTPBody)
	m := gemHTTPMap(t, []byte(body))
	u, _ := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiHTTPUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", gemHTTPBody)
	m := gemHTTPMap(t, []byte(body))
	e, _ := m["error"].(map[string]any)
	if code != 404 || e["code"] != float64(404) || e["status"] != "NOT_FOUND" || e["message"] == nil {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:bogus", gemHTTPBody)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown method: %d %s", code, body)
	}
}

func TestGeminiHTTPBadBody(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents": 5}`)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiHTTPCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, body := range []string{gemHTTPBody, `{"generateContentRequest": ` + gemHTTPBody + `}`} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		m := gemHTTPMap(t, []byte(out))
		if code != 200 {
			t.Fatalf("status %d: %s", code, out)
		}
		if n, _ := m["totalTokens"].(float64); n < 1 {
			t.Errorf("totalTokens: %s", out)
		}
	}
	code, out := post(t, "/v1beta/models/nope:countTokens", gemHTTPBody)
	if code != 404 || !strings.Contains(out, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, out)
	}
}

func TestGeminiHTTPModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	m := gemHTTPMap(t, rec.Body.Bytes())
	list, _ := m["models"].([]any)
	found := false
	for _, x := range list {
		e := x.(map[string]any)
		if e["name"] == "models/fake/m1" && e["displayName"] != nil {
			methods := e["supportedGenerationMethods"].([]any)
			if len(methods) == 3 {
				found = true
			}
		}
	}
	if rec.Code != 200 || !found {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiHTTPInfoListsAPIs(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "generateContent") || !strings.Contains(rec.Body.String(), "countTokens") {
		t.Errorf("%s", rec.Body.String())
	}
}
