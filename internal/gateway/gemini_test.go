package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiReq is a Gemini call with a system instruction, a function call and
// its result in the history, and a tool declaration with upper-case types.
const geminiReq = `{
  "systemInstruction": {"parts": [{"text": "be brief"}]},
  "contents": [
    {"role": "user", "parts": [{"text": "read a.go"}]},
    {"role": "model", "parts": [{"thought": true, "text": "need to read"}, {"functionCall": {"name": "read", "args": {"path": "x"}}}]},
    {"role": "user", "parts": [{"functionResponse": {"name": "read", "response": {"output": "package x"}}}]}
  ],
  "tools": [{"functionDeclarations": [{"name": "read", "description": "read a file",
     "parameters": {"type": "OBJECT", "properties": {"path": {"type": "STRING"}}, "required": ["path"]}}]}],
  "generationConfig": {"maxOutputTokens": 100, "thinkingConfig": {"thinkingBudget": -1, "includeThoughts": true}}
}`

// geminiReply is a Chat Completions stream: reasoning, text, a tool call
// whose arguments arrive in pieces, and usage with 2 reasoning tokens.
var geminiReply = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`)

// gemParts returns the parts of the first candidate of a Gemini response.
func gemParts(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	cands, ok := m["candidates"].([]any)
	if !ok || len(cands) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	content := cands[0].(map[string]any)["content"].(map[string]any)
	var out []map[string]any
	for _, p := range content["parts"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: geminiReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
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
		t.Fatalf("messages: %v", msgs)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] == "" || msgs[3].(map[string]any)["tool_call_id"] != call["id"] || msgs[3].(map[string]any)["content"] != "package x" {
		t.Errorf("function result not matched to its call: %v", msgs)
	}

	// what the client got: thought, text, one complete call, then the end
	evs := events(body)
	if len(evs) < 4 {
		t.Fatalf("too few events: %s", body)
	}
	var thoughts, texts []string
	var calls []map[string]any
	for _, e := range evs[:len(evs)-1] {
		for _, p := range gemParts(t, e) {
			switch {
			case p["thought"] == true:
				thoughts = append(thoughts, p["text"].(string))
			case p["functionCall"] != nil:
				calls = append(calls, p["functionCall"].(map[string]any))
			case p["text"] != nil:
				texts = append(texts, p["text"].(string))
			}
		}
	}
	if strings.Join(thoughts, "") != "hmm" || strings.Join(texts, "") != "Hi there" {
		t.Errorf("thought %v text %v", thoughts, texts)
	}
	if len(calls) != 1 || calls[0]["id"] != "call_9" || calls[0]["name"] != "read" {
		t.Fatalf("calls: %v", calls)
	}
	if args := calls[0]["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args: %v", args)
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finish: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: geminiReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	cand := out["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finish: %v", cand)
	}
	var found bool
	for _, p := range gemParts(t, out) {
		if fc, ok := p["functionCall"].(map[string]any); ok {
			found = fc["id"] == "call_9" && fc["args"].(map[string]any)["path"] == "a.go"
		}
	}
	if !found {
		t.Errorf("no complete functionCall: %s", body)
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
	if out["modelVersion"] == nil || out["responseId"] == nil {
		t.Errorf("missing modelVersion/responseId: %s", body)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "tools":[{"functionDeclarations":[{"name":"q","parameters":{"type":"OBJECT","properties":{"tags":{"type":"ARRAY","items":{"type":"STRING"}}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["q"]}},
	  "generationConfig":{"maxOutputTokens":321,"temperature":0.5,"stopSequences":["END"],
	    "responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"ok":{"type":"BOOLEAN"}}},
	    "thinkingConfig":{"thinkingBudget":-1}}}`
	code, reply := post(t, "/v1beta/models/fake/m1:generateContent", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["max_tokens"] != float64(321) {
		t.Errorf("max_tokens: %v", up["max_tokens"])
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort: %v", up["reasoning_effort"])
	}
	if up["temperature"] != 0.5 {
		t.Errorf("temperature: %v", up["temperature"])
	}
	params := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	tags := params["properties"].(map[string]any)["tags"].(map[string]any)
	if params["type"] != "object" || tags["type"] != "array" || tags["items"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	if tc, ok := up["tool_choice"].(map[string]any); !ok || tc["function"].(map[string]any)["name"] != "q" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
	msgs := up["messages"].([]any)
	sysMsg := msgs[0].(map[string]any)["content"].(string)
	if msgs[0].(map[string]any)["role"] != "system" || !strings.Contains(sysMsg, "sys") ||
		!strings.Contains(sysMsg, "JSON") || !strings.Contains(sysMsg, `"type":"boolean"`) {
		t.Errorf("system: %q", sysMsg)
	}
	if stop, ok := up["stop"].([]any); !ok || len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", geminiReq)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiModelWithColon(t *testing.T) {
	f := &fake{t: t, reply: geminiReply}
	setup(t, provider.Chat, f)
	p, _ := provider.Find("fake")
	p.Models = []string{"m:1"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1beta/models/fake/m:1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m:1" {
		t.Errorf("upstream model %v", up["model"])
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:countTokens",
		`{"contents":[{"role":"user","parts":[{"text":"hello world, this is a test"}]}]}`)
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out["totalTokens"] == nil || out["totalTokens"].(float64) <= 0 {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world, this is a test"}]}]}}`)
	out = nil
	json.Unmarshal([]byte(body), &out)
	if code != 200 || out["totalTokens"] == nil || out["totalTokens"].(float64) <= 0 {
		t.Errorf("wrapped: %d %s", code, body)
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) ||
		!strings.Contains(body, `"displayName"`) || !strings.Contains(body, `"streamGenerateContent"`) ||
		!strings.Contains(body, `"countTokens"`) {
		t.Errorf("%d %s", rec.Code, body)
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "/v1beta/models") {
		t.Errorf("root info does not list the Gemini API: %s", rec.Body.String())
	}
}

func TestGeminiErrorShape(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:bogus", strings.NewReader("{}")))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"status":"NOT_FOUND"`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `not json`)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("%d %s", code, body)
	}
}
