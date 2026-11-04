package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// toolUpstream is a Chat Completions upstream that thinks, says hi, then
// calls a tool whose arguments arrive in two fragments.
func toolUpstream(t *testing.T) *fake {
	return &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"plan"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
}

const geminiToolBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[
    {"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"t0","name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"t0","name":"read","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
  "generationConfig":{"maxOutputTokens":321,"temperature":0.3,"topP":0.9,"stopSequences":["END"],
    "thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

// geminiParts gathers the parts of every event, in order.
func geminiParts(t *testing.T, evs []map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range evs {
		cands, _ := e["candidates"].([]any)
		for _, c := range cands {
			cand, _ := c.(map[string]any)
			content, ok := cand["content"].(map[string]any)
			if !ok {
				continue
			}
			parts, _ := content["parts"].([]any)
			for _, p := range parts {
				out = append(out, p.(map[string]any))
			}
		}
	}
	return out
}

func get(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

func TestGeminiStreamToolCall(t *testing.T) {
	f := toolUpstream(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiToolBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" || up["stream"] != true {
		t.Errorf("upstream path %s, body %s", f.path, f.got)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("thinkingBudget -1 should be medium, got %v", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(321) || up["temperature"] != 0.3 || up["top_p"] != 0.9 {
		t.Errorf("generation params: %s", f.got)
	}
	if stop, _ := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	prop := params["properties"].(map[string]any)["path"].(map[string]any)
	if params["type"] != "object" || prop["type"] != "string" {
		t.Errorf("schema not lowercased: %v", params)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages: %v", msgs)
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "t0" {
		t.Errorf("assistant call id not kept: %v", calls)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "t0" {
		t.Errorf("tool result id: %v", msgs[3])
	}

	// what the client got: thinking, text, one whole function call, then the end
	evs := events(body)
	var got []string
	var call map[string]any
	for _, p := range geminiParts(t, evs) {
		switch {
		case p["thought"] == true:
			got = append(got, "think:"+p["text"].(string))
		case p["functionCall"] != nil:
			call = p["functionCall"].(map[string]any)
			got = append(got, "call:"+call["id"].(string)+":"+call["name"].(string))
		default:
			got = append(got, "text:"+p["text"].(string))
		}
	}
	if strings.Join(got, " ") != "think:plan text:Hi call:call_7:read" {
		t.Errorf("parts: %v", got)
	}
	if args, _ := call["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("function call args incomplete: %v", call["args"])
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", cand["finishReason"])
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateContentToolCall(t *testing.T) {
	f := toolUpstream(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiToolBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string           `json:"role"`
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata map[string]any `json:"usageMetadata"`
		ModelVersion  string         `json:"modelVersion"`
		ResponseID    string         `json:"responseId"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	c := out.Candidates[0]
	if c.FinishReason != "STOP" || c.Content.Role != "model" || out.ModelVersion != "fake/m1" || out.ResponseID == "" {
		t.Errorf("candidate: %s", body)
	}
	var calls []map[string]any
	for _, p := range c.Content.Parts {
		if fc, ok := p["functionCall"].(map[string]any); ok {
			calls = append(calls, fc)
		}
	}
	if len(calls) != 1 || calls[0]["id"] != "call_7" || calls[0]["name"] != "read" {
		t.Fatalf("functionCall: %s", body)
	}
	if args := calls[0]["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args: %v", args)
	}
	if out.UsageMetadata["promptTokenCount"] != float64(10) || out.UsageMetadata["candidatesTokenCount"] != float64(3) ||
		out.UsageMetadata["thoughtsTokenCount"] != float64(2) || out.UsageMetadata["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", out.UsageMetadata)
	}
}

func TestGeminiCallIDsMatchByName(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"content":"done"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[
	  {"role":"user","parts":[{"text":"go"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"ls","args":{"dir":"a"}}},{"functionCall":{"name":"ls","args":{"dir":"b"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"output":"x"}}}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages: %v", msgs)
	}
	calls := msgs[1].(map[string]any)["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("calls: %v", calls)
	}
	first, second := calls[0].(map[string]any)["id"], calls[1].(map[string]any)["id"]
	if first == "" || second == "" || first == second {
		t.Errorf("calls need distinct ids: %v %v", first, second)
	}
	// the result carries no id, so it belongs to the latest call named ls
	if msgs[2].(map[string]any)["tool_call_id"] != second {
		t.Errorf("result matched %v, want %v", msgs[2], second)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","model":"m1","choices":[{"delta":{"content":"{}"}}]}`,
		`data: {"id":"c3","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"application/pdf","data":"JVBER"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
	  "tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT"}}]}],
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(string(f.got), "data:image/png;base64,AAAA") {
		t.Errorf("inline image not passed: %s", f.got)
	}
	if !strings.Contains(string(f.got), "[attachment omitted: application/pdf]") {
		t.Errorf("non-image attachment not replaced by text: %s", f.got)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["tool_choice"] != "none" {
		t.Errorf("toolConfig NONE: %v", up["tool_choice"])
	}
	msgs := up["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "sys") || !strings.Contains(sys, "only") || !strings.Contains(sys, `"type":"object"`) {
		t.Errorf("JSON mode instruction missing or schema not lowercased: %q", sys)
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, body := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}`,
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}}`,
	} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		var m map[string]any
		json.Unmarshal([]byte(out), &m)
		if code != 200 {
			t.Fatalf("status %d: %s", code, out)
		}
		if n, ok := m["totalTokens"].(float64); !ok || n < 1 {
			t.Errorf("totalTokens: %s", out)
		}
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := get(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	json.Unmarshal([]byte(body), &out)
	found := false
	for _, m := range out.Models {
		if m.Name == "models/fake/m1" {
			found = true
			if m.DisplayName == "" || len(m.SupportedGenerationMethods) != 3 {
				t.Errorf("model object: %+v", m)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
	if _, root := get(t, "/"); !strings.Contains(root, "/v1beta/models") {
		t.Errorf("root does not list the Gemini API: %s", root)
	}
	if code, _ := get(t, "/v1beta/models/fake/m1"); code != 200 {
		t.Errorf("GET model: %d", code)
	}
}

func TestGeminiEffort(t *testing.T) {
	ptr := func(n int) *int { return &n }
	cases := []struct {
		budget *int
		level  string
		want   string
	}{
		{ptr(-1), "", "medium"},
		{ptr(0), "", ""},
		{ptr(2000), "", "low"},
		{ptr(8000), "", "medium"},
		{ptr(30000), "", "high"},
		{nil, "HIGH", "high"},
		{nil, "", ""},
	}
	for _, c := range cases {
		if got := geminiEffort(c.budget, c.level); got != c.want {
			t.Errorf("effort(%v, %q) = %q, want %q", c.budget, c.level, got, c.want)
		}
	}
}

func TestGeminiLowerSchema(t *testing.T) {
	in := `{"type":"OBJECT","propertyOrdering":["tags"],"properties":{"tags":{"type":"ARRAY","items":{"type":"STRING"}},"n":{"type":"INTEGER","nullable":true}}}`
	var out map[string]any
	if err := json.Unmarshal(lowerSchema([]byte(in)), &out); err != nil {
		t.Fatal(err)
	}
	props := out["properties"].(map[string]any)
	tags := props["tags"].(map[string]any)
	if out["type"] != "object" || tags["type"] != "array" || tags["items"].(map[string]any)["type"] != "string" {
		t.Errorf("lowered: %v", out)
	}
	if _, ok := out["propertyOrdering"]; ok {
		t.Errorf("propertyOrdering kept: %v", out)
	}
	if n := props["n"].(map[string]any)["type"]; n == nil || n == "integer" {
		t.Errorf("nullable not expressed: %v", n)
	}
}

func TestGeminiUsageMapping(t *testing.T) {
	u := Usage{Input: 10, Output: 5, Reasoning: 2}
	m := u.gemini()
	if m["promptTokenCount"] != 10 || m["candidatesTokenCount"] != 3 || m["thoughtsTokenCount"] != 2 || m["totalTokenCount"] != 15 {
		t.Errorf("usage: %v", m)
	}
	if _, ok := m["cachedContentTokenCount"]; ok {
		t.Errorf("no cache, no cached count: %v", m)
	}
	c := Usage{Input: 4, CacheRead: 6, Output: 2}.gemini()
	if c["promptTokenCount"] != 10 || c["cachedContentTokenCount"] != 6 {
		t.Errorf("cached prompt should include cache: %v", c)
	}
}

// A non-streaming reply whose arguments were cut short must still be valid
// JSON: the arguments are wrapped rather than emitted broken.
func TestGeminiGenerateContentTruncatedArgs(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c5","model":"m1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c5","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("reply is not valid JSON: %s", body)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					FunctionCall *struct {
						ID   string          `json:"id"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 1 || parts[0].FunctionCall == nil {
		t.Fatalf("want one functionCall: %s", body)
	}
	if !json.Valid(parts[0].FunctionCall.Args) {
		t.Errorf("args are not valid JSON: %s", parts[0].FunctionCall.Args)
	}
}

// A result whose id the gateway never issued is matched to the latest call
// with the same name, so the upstream sees the id it was given.
func TestGeminiResultIDMapsToGeneratedCall(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c6","model":"m1","choices":[{"delta":{"content":"done"}}]}`,
		`data: {"id":"c6","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[
	  {"role":"user","parts":[{"text":"go"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"ls","args":{"dir":"a"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"id":"client-7","name":"ls","response":{"output":"x"}}}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages: %v", msgs)
	}
	calls := msgs[1].(map[string]any)["tool_calls"].([]any)
	gen := calls[0].(map[string]any)["id"]
	if gen == "" || gen == "client-7" {
		t.Fatalf("call should carry a generated id, got %v", gen)
	}
	if got := msgs[2].(map[string]any)["tool_call_id"]; got != gen {
		t.Errorf("result id %v, want generated %v", got, gen)
	}
}
