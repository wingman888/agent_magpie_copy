package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

const geminiGen = "/v1beta/models/fake/m1:generateContent"

// geminiPartsOf is the parts of the first candidate of one stream event.
func geminiPartsOf(ev map[string]any) []map[string]any {
	var out []map[string]any
	cands, _ := ev["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	c0, _ := cands[0].(map[string]any)
	content, _ := c0["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestGeminiGenerateThroughChat(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi "}}]}`,
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"completion_tokens_details":{"reasoning_tokens":1}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", geminiGen, strings.NewReader(`{"systemInstruction":{"parts":[{"text":"be brief"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":50,"temperature":0.5}}`))
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, want := range []string{`"text":"hi there"`, `"finishReason":"STOP"`, `"usageMetadata"`, `"promptTokenCount":7`,
		`"candidatesTokenCount":3`, `"thoughtsTokenCount":1`, `"modelVersion":"fake/m1"`, `"role":"model"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	if !strings.Contains(string(f.got), `"role":"system"`) || !strings.Contains(string(f.got), `"content":"be brief"`) || !strings.Contains(string(f.got), `"max_tokens":50`) {
		t.Errorf("upstream body: %s", f.got)
	}
	if recent := s.Recent(); len(recent) != 1 || recent[0].From != geminiProto || recent[0].Status != 200 {
		t.Errorf("call log: %+v", recent)
	}
}

func TestGeminiStreamKeepsOrderAndToolCall(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		`data: {"id":"c2","model":"m1","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`data: {"id":"c2","model":"m1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`data: {"id":"c2","model":"m1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`data: {"id":"c2","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"weather"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var kinds []string
	var last map[string]any
	for _, ev := range events(body) {
		for _, p := range geminiPartsOf(ev) {
			switch {
			case p["thought"] == true:
				kinds = append(kinds, "thought:"+p["text"].(string))
			case p["functionCall"] != nil:
				fc := p["functionCall"].(map[string]any)
				args, _ := json.Marshal(fc["args"])
				kinds = append(kinds, "call:"+fc["id"].(string)+":"+fc["name"].(string)+":"+string(args))
			default:
				kinds = append(kinds, "text:"+p["text"].(string))
			}
		}
		last = ev
	}
	want := []string{"thought:think", "text:ok", `call:call_9:get_weather:{"city":"Paris"}`}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Errorf("parts %v, want %v", kinds, want)
	}
	if last == nil {
		t.Fatal("no events")
	}
	raw, _ := json.Marshal(last)
	for _, w := range []string{`"finishReason":"STOP"`, `"thoughtsTokenCount":2`, `"promptTokenCount":4`} {
		if !strings.Contains(string(raw), w) {
			t.Errorf("last event lacks %s: %s", w, raw)
		}
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","model":"m1","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{
	  "systemInstruction":{"parts":[{"text":"sys rules"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"weather?"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},
	    {"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"output":"sunny"}}}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"get_weather","description":"w","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}
	}`
	if code, out := post(t, geminiGen, body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var up struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice      map[string]any `json:"tool_choice"`
		ReasoningEffort string         `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body %s: %v", f.got, err)
	}
	if len(up.Messages) != 4 {
		t.Fatalf("messages: %s", f.got)
	}
	if up.Messages[0].Role != "system" || up.Messages[0].Content != "sys rules" {
		t.Errorf("system message: %+v", up.Messages[0])
	}
	if !strings.Contains(string(f.got), `"url":"data:image/png;base64,AAAA"`) {
		t.Errorf("image not forwarded: %s", f.got)
	}
	call := up.Messages[2].ToolCalls
	if len(call) != 1 || call[0].ID == "" {
		t.Fatalf("assistant tool calls: %+v", up.Messages[2])
	}
	if up.Messages[3].Role != "tool" || up.Messages[3].ToolCallID != call[0].ID || up.Messages[3].Content != "sunny" {
		t.Errorf("tool result not paired with its call: %+v", up.Messages[3])
	}
	props := up.Tools[0].Function.Parameters["properties"].(map[string]any)
	if up.Tools[0].Function.Parameters["type"] != "object" || props["city"].(map[string]any)["type"] != "string" {
		t.Errorf("schema types not lower-cased: %v", up.Tools[0].Function.Parameters)
	}
	if up.ReasoningEffort != "medium" {
		t.Errorf("reasoning effort %q", up.ReasoningEffort)
	}
	if fn, _ := up.ToolChoice["function"].(map[string]any); fn["name"] != "get_weather" {
		t.Errorf("tool choice %v", up.ToolChoice)
	}
}

func TestGeminiNotFoundCountAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, count me"}]}]}}`)
	if code != 200 || !strings.Contains(body, `{"totalTokens":`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) || !strings.Contains(rec.Body.String(), `"supportedGenerationMethods"`) {
		t.Errorf("models: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") || !strings.Contains(rec.Body.String(), "streamGenerateContent") {
		t.Errorf("info: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404 hint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiJSONModeAndUsageLog(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c4","model":"m1","choices":[{"index":0,"delta":{"content":"{\"a\":\"x\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, geminiGen, `{"contents":[{"role":"user","parts":[{"text":"give json"}]}],"generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)
	if code != 200 || !strings.Contains(body, `{\"a\":\"x\"}`) && !strings.Contains(body, `"text":"{\"a\":\"x\"}"`) {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(f.got), "response_format") {
		t.Errorf("upstream got a JSON-mode field: %s", f.got)
	}
	if !strings.Contains(string(f.got), "只输出符合以下 schema 的 JSON") || !strings.Contains(string(f.got), `"type":"object"`) {
		t.Errorf("JSON instruction missing: %s", f.got)
	}
	recs := usage.Load(time.Now().Add(-time.Hour))
	found := false
	for _, r := range recs {
		if r.Provider == "fake" && r.Model == "m1" && r.Input == 3 && r.Output == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("usage not logged: %+v", recs)
	}
}
