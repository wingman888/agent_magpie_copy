package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gpost sends a request through a server's handler.
func gpost(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// geminiParts is the parts of the first candidate of a Gemini reply or chunk.
func geminiParts(t *testing.T, v map[string]any) []map[string]any {
	t.Helper()
	cs, _ := v["candidates"].([]any)
	if len(cs) == 0 {
		t.Fatalf("no candidates: %v", v)
	}
	content := cs[0].(map[string]any)["content"].(map[string]any)
	var out []map[string]any
	for _, p := range content["parts"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiGenerateContentText(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":" world"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	srv := New()
	code, body := gpost(t, srv, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	ps := geminiParts(t, out)
	if len(ps) != 1 || ps[0]["text"] != "Hello world" {
		t.Errorf("parts: %v", ps)
	}
	cand := out["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("finish: %s", body)
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
	if out["modelVersion"] != "fake/m1" || out["responseId"] == nil {
		t.Errorf("ids: %s", body)
	}
	if len(srv.Recent()) != 1 || srv.Recent()[0].From != "gemini" || srv.Recent()[0].Status != 200 {
		t.Errorf("call log: %+v", srv.Recent())
	}
}

func TestGeminiStreamOrder(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 3 {
		t.Fatalf("%d events: %s", len(evs), body)
	}
	if p := geminiParts(t, evs[0])[0]; p["thought"] != true || p["text"] != "hmm" {
		t.Errorf("first part: %v", p)
	}
	if p := geminiParts(t, evs[1])[0]; p["text"] != "Hi" || p["thought"] != nil {
		t.Errorf("second part: %v", p)
	}
	last := evs[2]
	fc := geminiParts(t, last)[0]["functionCall"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call: %v", fc)
	}
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finish: %v", cand)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["thoughtsTokenCount"] != float64(3) || um["promptTokenCount"] != float64(4) {
		t.Errorf("usage: %v", um)
	}

	// without alt=sse the chunks come back as one JSON array
	_, arr := gpost(t, New(), "/v1beta/models/fake/m1:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	var chunks []map[string]any
	if err := json.Unmarshal([]byte(arr), &chunks); err != nil || len(chunks) != 3 {
		t.Errorf("array form: %v %s", err, arr)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]},
	    {"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"text":"what is it"}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":100,"temperature":0.5,"stopSequences":["END"],"thinkingConfig":{"thinkingBudget":-1}}
	}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatal(err)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages: %s", f.got)
	}
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system: %v", sys)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] == "" || call["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool call: %v", call)
	}
	res := msgs[3].(map[string]any)
	if res["role"] != "tool" || res["tool_call_id"] != call["id"] || res["content"] != "package x" {
		t.Errorf("tool result not paired: %v", res)
	}
	img := msgs[4].(map[string]any)["content"].([]any)[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(img, "data:image/png;base64,AAAA") {
		t.Errorf("image: %s", img)
	}
	params := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	if tc := up["tool_choice"].(map[string]any)["function"].(map[string]any); tc["name"] != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("effort: %v", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(100) || up["temperature"] != 0.5 {
		t.Errorf("params: %s", f.got)
	}
	if stop, _ := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
}

func TestGeminiJSONMode(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"{}"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"is it?"}]}],
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"ok":{"type":"BOOLEAN"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if _, ok := up["response_format"]; ok {
		t.Errorf("response_format should not be set: %s", f.got)
	}
	sys := up["messages"].([]any)[0].(map[string]any)
	text, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(text, "JSON") || !strings.Contains(text, `"type":"boolean"`) {
		t.Errorf("json instruction: %v", sys)
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := gpost(t, New(), "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.HasPrefix(body, `{"error":{"code":404,`) || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiUpstreamError(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance","type":"x"}}`}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:generateContent", `{"contents":[]}`)
	if code != 402 || !strings.Contains(body, "Fake: Insufficient Balance") || !strings.Contains(body, `"code":402`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	srv := New()
	// 26 characters of text, a quarter of that
	code, body := gpost(t, srv, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"abcdefghijklmnopqrstuvwxyz"}]}]}`)
	if code != 200 || body != `{"totalTokens":6}` {
		t.Errorf("direct: %d %s", code, body)
	}
	code, body = gpost(t, srv, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"abcdefghijklmnopqrstuvwxyz"}]}]}}`)
	if code != 200 || body != `{"totalTokens":6}` {
		t.Errorf("wrapped: %d %s", code, body)
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) || !strings.Contains(body, `"supportedGenerationMethods":["generateContent"`) {
		t.Errorf("%d %s", rec.Code, body)
	}
}

func TestGeminiRootMentionsAPI(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("info: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404 hint: %s", rec.Body.String())
	}
}

func TestGeminiStreamWithoutContentType(t *testing.T) {
	f := &fake{t: t, noType: true, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 2 {
		t.Fatalf("%d events: %s", len(evs), body)
	}
	if p := geminiParts(t, evs[0])[0]; p["text"] != "Hi" {
		t.Errorf("first part: %v", p)
	}
	last := evs[1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finish: %v", cand)
	}
	if um := last["usageMetadata"].(map[string]any); um["promptTokenCount"] != float64(3) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiAnyMultipleNamesFiltersTools(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := gpost(t, New(), "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"do it"}]}],
	  "tools":[{"functionDeclarations":[
	    {"name":"read","parameters":{"type":"OBJECT","properties":{}}},
	    {"name":"write","parameters":{"type":"OBJECT","properties":{}}},
	    {"name":"delete","parameters":{"type":"OBJECT","properties":{}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read","write"]}}
	}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatal(err)
	}
	tools, _ := up["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools not filtered to the allowed names: %s", f.got)
	}
	for _, tl := range tools {
		name := tl.(map[string]any)["function"].(map[string]any)["name"]
		if name != "read" && name != "write" {
			t.Errorf("unexpected tool %v", name)
		}
	}
	if up["tool_choice"] != "required" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}
