package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

type fakeUp struct {
	got  []byte
	path string
	code int
	body string
}

func (f *fakeUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path = r.URL.Path
	w.Header().Set("Content-Type", "text/event-stream")
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.body)
}

func sseBody(lines ...string) string {
	return strings.Join(lines, "\n\n") + "\n\n"
}

func setupUp(t *testing.T, proto provider.Protocol, f *fakeUp) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func doReq(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	return rec.Code, rec.Body.String()
}

func sseEvents(body string) []map[string]any {
	var out []map[string]any
	for _, ln := range strings.Split(body, "\n") {
		d, ok := strings.CutPrefix(ln, "data:")
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(d)), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func chatToolStream(finish string, usage string) string {
	return sseBody(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Sure"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"`+finish+`"}]}`,
		`data: {"id":"c1","choices":[],"usage":`+usage+`}`,
		`data: [DONE]`)
}

const geminiReq = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"fc0","name":"read_file","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"fc0","name":"read_file","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func TestAcceptGeminiStreamToChat(t *testing.T) {
	f := &fakeUp{body: chatToolStream("tool_calls", `{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}`)}
	setupUp(t, provider.Chat, f)
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m1" || up["stream"] != true || up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) {
		t.Errorf("upstream: %s", f.got)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["content"] != "be brief" || msgs[3].(map[string]any)["tool_call_id"] != "fc0" {
		t.Errorf("messages: %v", msgs)
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	pj, _ := json.Marshal(fn["parameters"])
	if !strings.Contains(string(pj), `"type":"object"`) || strings.Contains(string(pj), "OBJECT") || strings.Contains(string(pj), "STRING") {
		t.Errorf("schema not lowercased: %s", pj)
	}
	evs := sseEvents(body)
	var parts []map[string]any
	var finish string
	for _, e := range evs {
		c := e["candidates"].([]any)[0].(map[string]any)
		if ct, ok := c["content"].(map[string]any); ok {
			for _, p := range ct["parts"].([]any) {
				parts = append(parts, p.(map[string]any))
			}
		}
		if fr, ok := c["finishReason"]; ok {
			finish = fr.(string)
		}
	}
	if len(parts) != 3 || parts[0]["thought"] != true || parts[1]["text"] != "Sure" {
		t.Fatalf("parts: %v", parts)
	}
	fc, _ := parts[2]["functionCall"].(map[string]any)
	if fc == nil || fc["name"] != "read_file" || fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call: %v", parts[2])
	}
	um := evs[len(evs)-1]["usageMetadata"].(map[string]any)
	if finish != "STOP" || um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("finish/usage: %s %v", finish, um)
	}
}

func TestAcceptGeminiNonStreamAndErrors(t *testing.T) {
	f := &fakeUp{body: chatToolStream("tool_calls", `{"prompt_tokens":10,"completion_tokens":5}`)}
	setupUp(t, provider.Chat, f)
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"name":"read_file"`) {
		t.Errorf("generateContent: %d %s", code, body)
	}
	code, body = doReq(t, "POST", "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, _ = doReq(t, "POST", "/v1beta/models/fake-m1", `{}`)
	if code != 404 {
		t.Errorf("no colon: %d", code)
	}
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:bogus", `{}`)
	if code != 404 || !strings.Contains(body, "unknown method bogus") {
		t.Errorf("unknown method: %d %s", code, body)
	}
	// model ids may contain colons: split at the last one
	code, body = doReq(t, "POST", "/v1beta/models/ollama/llama3:8b:bogus", `{}`)
	if code != 404 || !strings.Contains(body, "unknown method bogus") {
		t.Errorf("last colon split: %d %s", code, body)
	}
}

func TestAcceptGeminiCountTokensAndModels(t *testing.T) {
	setupUp(t, provider.Chat, &fakeUp{})
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:countTokens", geminiReq)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":`+geminiReq+`}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) || strings.Contains(body, `"totalTokens":0`) {
		t.Errorf("wrapped countTokens: %d %s", code, body)
	}
	code, _ = doReq(t, "POST", "/v1beta/models/fake/m1:countTokens", `{bad`)
	if code != 400 {
		t.Errorf("bad json: %d", code)
	}
	code, body = doReq(t, "GET", "/v1beta/models", "")
	if code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) || !strings.Contains(body, "generateContent") || !strings.Contains(body, "via dial") {
		t.Errorf("models: %d %s", code, body)
	}
}

func TestAcceptGeminiFinishAndCache(t *testing.T) {
	f := &fakeUp{body: sseBody(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`,
		`data: [DONE]`)}
	setupUp(t, provider.Chat, f)
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) || !strings.Contains(body, `"cachedContentTokenCount":4`) {
		t.Errorf("%d %s", code, body)
	}
	if !strings.Contains(body, `"promptTokenCount":10`) || !strings.Contains(body, `"totalTokenCount":15`) {
		t.Errorf("usage: %s", body)
	}
}

func TestAcceptGeminiRequestTranslation(t *testing.T) {
	f := &fakeUp{body: sseBody(`data: {"id":"c1","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `data: [DONE]`)}
	setupUp(t, provider.Chat, f)
	req := `{"systemInstruction":"sys",
	  "contents":[{"role":"user","parts":[{"text":"go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read_file","args":{}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"output":"R"}}},
	      {"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read_file"},{"name":"other"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"any","allowedFunctionNames":["read_file"]}},
	  "generationConfig":{"responseMimeType":"application/json"}}`
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", req)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || !strings.HasPrefix(first["content"].(string), "sys") || !strings.Contains(strings.ToLower(first["content"].(string)), "json") {
		t.Errorf("system: %v", first)
	}
	// the call generated an id; the tool result must refer to it
	var callID, resultID string
	for _, m := range msgs {
		mm := m.(map[string]any)
		if tcs, ok := mm["tool_calls"].([]any); ok {
			callID, _ = tcs[0].(map[string]any)["id"].(string)
		}
		if mm["role"] == "tool" {
			resultID, _ = mm["tool_call_id"].(string)
		}
	}
	if callID == "" || callID != resultID {
		t.Errorf("name-matched ids: %q vs %q in %v", callID, resultID, msgs)
	}
	if !strings.Contains(string(f.got), "image_url") || !strings.Contains(string(f.got), "image/png") {
		t.Errorf("image missing: %s", f.got)
	}
	tc, _ := json.Marshal(up["tool_choice"])
	if !strings.Contains(string(tc), "read_file") {
		t.Errorf("tool_choice: %s", tc)
	}
}

func TestAcceptGeminiErrorResultToAnthropic(t *testing.T) {
	f := &fakeUp{body: sseBody(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","model":"m1","usage":{"input_tokens":1}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	setupUp(t, provider.Anthropic, f)
	req := `{"contents":[{"role":"user","parts":[{"text":"go"}]},
	    {"role":"model","parts":[{"functionCall":{"id":"x1","name":"run","args":{}}}]},
	    {"role":"user","parts":[{"functionResponse":{"id":"x1","name":"run","response":{"error":"boom"}}}]}]}`
	doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", req)
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/messages" {
		t.Fatalf("path %q body %s", f.path, f.got)
	}
	if !strings.Contains(string(f.got), `"is_error":true`) || !strings.Contains(string(f.got), "boom") || !strings.Contains(string(f.got), `"tool_use_id":"x1"`) {
		t.Errorf("error result: %s", f.got)
	}
}
