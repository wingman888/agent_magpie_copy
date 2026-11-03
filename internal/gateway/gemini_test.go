package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiCandidate returns the first candidate of a Gemini response object.
func geminiCandidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cs, ok := m["candidates"].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	return cs[0].(map[string]any)
}

// geminiParts returns the parts of a candidate.
func geminiParts(c map[string]any) []map[string]any {
	var out []map[string]any
	content, _ := c["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestGeminiPathSplit(t *testing.T) {
	cases := []struct {
		rest, model, method string
		ok                  bool
	}{
		{"fake/m1:generateContent", "fake/m1", "generateContent", true},
		{"a:b:streamGenerateContent", "a:b", "streamGenerateContent", true},
		{"fake/m1:countTokens", "fake/m1", "countTokens", true},
		{"fake/m1", "", "", false},
		{":generateContent", "", "", false},
	}
	for _, c := range cases {
		model, method, ok := splitGeminiPath(c.rest)
		if model != c.model || method != c.method || ok != c.ok {
			t.Errorf("splitGeminiPath(%q) = %q %q %v, want %q %q %v", c.rest, model, method, ok, c.model, c.method, c.ok)
		}
	}
}

// geminiReply is the upstream script shared by the generation tests: a
// reasoning fragment, text, a tool call whose arguments arrive in pieces,
// and usage of 10 prompt, 5 completion of which 2 are reasoning.
func geminiReply() string {
	return sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)
}

const geminiToolBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[
    {"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"t0","name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"t0","name":"read","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func TestGeminiStreamToolCall(t *testing.T) {
	f := &fake{t: t, reply: geminiReply()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiToolBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" || up["stream"] != true {
		t.Errorf("upstream path %s: %s", f.path, f.got)
	}
	if up["max_tokens"] != float64(100) || up["reasoning_effort"] != "medium" {
		t.Errorf("params: max_tokens=%v reasoning_effort=%v", up["max_tokens"], up["reasoning_effort"])
	}
	if strings.Contains(string(f.got), "OBJECT") || strings.Contains(string(f.got), "STRING") || !strings.Contains(string(f.got), `"type":"object"`) {
		t.Errorf("schema types not lowered: %s", f.got)
	}
	msgs, _ := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" ||
		msgs[3].(map[string]any)["tool_call_id"] != "t0" {
		t.Errorf("messages: %s", f.got)
	}

	// what the client got
	evs := events(body)
	var thought, text, calls int
	var last map[string]any
	for _, e := range evs {
		c := geminiCandidate(t, e)
		for _, p := range geminiParts(c) {
			if p["thought"] == true && p["text"] == "hmm" {
				thought++
			}
			if p["text"] != nil && p["thought"] == nil {
				text++
			}
			if fc, ok := p["functionCall"].(map[string]any); ok {
				calls++
				args, _ := fc["args"].(map[string]any)
				if fc["name"] != "read" || fc["id"] != "call_1" || args["path"] != "a.go" {
					t.Errorf("function call: %v", fc)
				}
			}
		}
		last = e
	}
	if thought != 1 || text != 2 || calls != 1 {
		t.Errorf("thought %d text %d calls %d in %d events: %s", thought, text, calls, len(evs), body)
	}
	lc := geminiCandidate(t, last)
	um, _ := last["usageMetadata"].(map[string]any)
	if lc["finishReason"] != "STOP" || um["promptTokenCount"] != float64(10) ||
		um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("last event: %v", last)
	}
}

func TestGeminiGenerateContent(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: geminiReply()})
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiToolBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	c := geminiCandidate(t, out)
	if c["finishReason"] != "STOP" || out["modelVersion"] != "fake/m1" {
		t.Errorf("candidate: %v", c)
	}
	var calls int
	for _, p := range geminiParts(c) {
		if fc, ok := p["functionCall"].(map[string]any); ok {
			calls++
			args, _ := fc["args"].(map[string]any)
			if fc["id"] != "call_1" || args["path"] != "a.go" {
				t.Errorf("function call: %v", fc)
			}
		}
	}
	um, _ := out["usageMetadata"].(map[string]any)
	if calls != 1 || um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("calls %d usage %v: %s", calls, um, body)
	}
}

func TestGeminiErrorsAreNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	ok := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	cases := []struct {
		path string
		code int
		want string
	}{
		{"/v1beta/models/nope:generateContent", 404, "NOT_FOUND"},
		{"/v1beta/models/fake/m1:embedContent", 404, "NOT_FOUND"},
		{"/v1beta/models/fake/m1:streamGenerateContent", 400, "INVALID_ARGUMENT"},
		{"/v1beta/models/nope:countTokens", 404, "NOT_FOUND"},
	}
	for _, c := range cases {
		code, body := post(t, c.path, ok)
		var v struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		json.Unmarshal([]byte(body), &v)
		if code != c.code || v.Error.Status != c.want || v.Error.Code != c.code || v.Error.Message == "" {
			t.Errorf("%s: %d %s", c.path, code, body)
		}
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	found := false
	for _, m := range out.Models {
		if m.Name == "models/fake/m1" {
			found = true
			if m.DisplayName == "" || len(m.Methods) != 3 {
				t.Errorf("model: %+v", m)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 missing: %s", rec.Body.String())
	}
}

func TestGeminiCountTokensBodies(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	direct := `{"contents":[{"role":"user","parts":[{"text":"abcdefgh"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"abcdefgh"}]}]}}`
	for _, b := range []string{direct, wrapped} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		if code != 200 || out["totalTokens"] != float64(2) {
			t.Errorf("%d %s", code, body)
		}
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"AAA"}},{"inlineData":{"mimeType":"application/pdf","data":"BBB"}}]},
	    {"role":"model","parts":[{"thought":true,"text":"think"},{"functionCall":{"name":"ls","args":{"dir":"."}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"output":"a b"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"ls","description":"list","parameters":{"type":"OBJECT","properties":{"dir":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["ls"]}},
	  "generationConfig":{"maxOutputTokens":77,"responseMimeType":"application/json","thinkingConfig":{"thinkingLevel":"HIGH"}}}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.System, "sys") || !strings.Contains(r.System, "JSON") {
		t.Errorf("system: %q", r.System)
	}
	if len(r.Messages) != 3 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	m0 := r.Messages[0].Parts
	if len(m0) != 3 || m0[0].Text != "hi" || m0[1].Kind != Image || m0[1].MediaType != "image/png" ||
		m0[2].Kind != Text || m0[2].Text != "[attachment: application/pdf]" {
		t.Errorf("user parts: %+v", m0)
	}
	m1 := r.Messages[1].Parts
	if r.Messages[1].Role != "assistant" || len(m1) != 2 || m1[0].Kind != Thinking || m1[0].Text != "think" ||
		m1[1].Kind != ToolCall || m1[1].Name != "ls" || !strings.HasPrefix(m1[1].ID, "call_") {
		t.Fatalf("model parts: %+v", m1)
	}
	m2 := r.Messages[2].Parts
	if len(m2) != 1 || m2[0].Kind != ToolResult || m2[0].CallID != m1[1].ID || m2[0].Text != "a b" {
		t.Errorf("result: %+v", m2)
	}
	if r.ToolChoice != "name:ls" || len(r.Tools) != 1 || !strings.Contains(string(r.Tools[0].Schema), `"type":"object"`) {
		t.Errorf("tools: %q %+v", r.ToolChoice, r.Tools)
	}
	if r.MaxTokens != 77 || r.Effort != "high" || !r.Thinking {
		t.Errorf("generation: max %d effort %q thinking %v", r.MaxTokens, r.Effort, r.Thinking)
	}
}
