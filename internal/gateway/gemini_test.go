package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiParts returns the parts of the first candidate of a Gemini reply.
func geminiParts(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Candidates) == 0 {
		t.Fatalf("not a Gemini reply: %s", body)
	}
	return out.Candidates[0].Content.Parts
}

func geminiFinishOf(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	json.Unmarshal([]byte(body), &out)
	if len(out.Candidates) == 0 {
		return ""
	}
	return out.Candidates[0].FinishReason
}

// upstreamMsgs decodes the messages a Chat upstream received.
func upstreamMsgs(t *testing.T, got []byte) []map[string]any {
	t.Helper()
	var up struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &up); err != nil {
		t.Fatalf("upstream body: %v: %s", err, got)
	}
	return up.Messages
}

const geminiText = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

func TestGeminiGenerateChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	parts := geminiParts(t, body)
	if len(parts) != 1 || parts[0]["text"] != "Hi there" {
		t.Errorf("parts: %v", parts)
	}
	if geminiFinishOf(t, body) != "STOP" || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("finish: %s", body)
	}
	if !strings.Contains(body, `"usageMetadata":{"candidatesTokenCount":5,"promptTokenCount":10,"thoughtsTokenCount":0,"totalTokenCount":15}`) {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiGenerateResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hi there"}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/responses" || geminiParts(t, body)[0]["text"] != "Hi there" || geminiFinishOf(t, body) != "STOP" {
		t.Errorf("%s %s", f.path, body)
	}
	if !strings.Contains(body, `"promptTokenCount":10`) || !strings.Contains(body, `"candidatesTokenCount":5`) {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiGenerateAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"m1","model":"m1","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi there"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/messages" || geminiParts(t, body)[0]["text"] != "Hi there" || geminiFinishOf(t, body) != "STOP" {
		t.Errorf("%s %s", f.path, body)
	}
	if !strings.Contains(body, `"candidatesTokenCount":5`) {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiStreamOrderAndIDs(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiText)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) < 4 {
		t.Fatalf("too few events: %s", body)
	}
	// each event carries one part; the thought comes first, then text, then the call
	var kinds []string
	var call map[string]any
	for _, ev := range evs[:len(evs)-1] {
		cand := ev["candidates"].([]any)[0].(map[string]any)
		parts := cand["content"].(map[string]any)["parts"].([]any)
		if len(parts) != 1 {
			t.Fatalf("event should carry one part: %v", ev)
		}
		p := parts[0].(map[string]any)
		switch {
		case p["thought"] == true:
			kinds = append(kinds, "think")
		case p["functionCall"] != nil:
			kinds = append(kinds, "call")
			call = p["functionCall"].(map[string]any)
		case p["text"] != nil:
			kinds = append(kinds, "text")
		}
	}
	if strings.Join(kinds, ",") != "think,text,text,call" {
		t.Errorf("order: %v", kinds)
	}
	if call["id"] != "call_1" || call["name"] != "read" {
		t.Errorf("call: %v", call)
	}
	if args, _ := call["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args: %v", call["args"])
	}
	last := evs[len(evs)-1]
	lastJSON, _ := json.Marshal(last)
	if !strings.Contains(string(lastJSON), `"finishReason":"STOP"`) ||
		!strings.Contains(string(lastJSON), `"thoughtsTokenCount":3`) ||
		!strings.Contains(string(lastJSON), `"promptTokenCount":10`) ||
		!strings.Contains(string(lastJSON), `"candidatesTokenCount":5`) {
		t.Errorf("last event: %s", lastJSON)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	msgs := upstreamMsgs(t, f.got)
	if len(msgs) != 4 || msgs[0]["role"] != "system" || msgs[0]["content"] != "be brief" {
		t.Fatalf("messages: %v", msgs)
	}
	calls, _ := msgs[2]["tool_calls"].([]any)
	if msgs[2]["role"] != "assistant" || len(calls) != 1 {
		t.Fatalf("assistant turn: %v", msgs[2])
	}
	id := calls[0].(map[string]any)["id"]
	if id == "" || msgs[3]["role"] != "tool" || msgs[3]["tool_call_id"] != id || msgs[3]["content"] != "package x" {
		t.Errorf("call and result not paired: %v / %v", msgs[2], msgs[3])
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	tools := up["tools"].([]any)
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)["path"].(map[string]any)
	if params["type"] != "object" || props["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("thinkingBudget -1 should be medium: %v", up["reasoning_effort"])
	}
	tc := up["tool_choice"].(map[string]any)
	if tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("ANY with one function should name it: %v", up["tool_choice"])
	}
}

func TestGeminiToolChoiceNoneAndAnyMany(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	tools := `"tools":[{"functionDeclarations":[{"name":"a"}]}]`
	cases := map[string]string{
		"NONE": `"none"`,
		"AUTO": `"auto"`,
		"ANY":  `"required"`,
	}
	for mode, want := range cases {
		body := `{"contents":[{"role":"user","parts":[{"text":"x"}]}],` + tools +
			`,"toolConfig":{"functionCallingConfig":{"mode":"` + mode + `"}}}`
		if code, b := post(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
			t.Fatalf("%s: %d %s", mode, code, b)
		}
		var up map[string]any
		json.Unmarshal(f.got, &up)
		if got, _ := json.Marshal(up["tool_choice"]); string(got) != want {
			t.Errorf("%s: tool_choice %s, want %s", mode, got, want)
		}
	}
	// ANY with two allowed functions asks for some call, not a named one
	body := `{"contents":[{"role":"user","parts":[{"text":"x"}]}],` + tools +
		`,"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a","b"]}}}`
	post(t, "/v1beta/models/fake/m1:generateContent", body)
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["tool_choice"] != "required" {
		t.Errorf("ANY with two names: %v", up["tool_choice"])
	}
}

func TestGeminiJSONModeAddsInstruction(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"id":"c1","choices":[{"delta":{"content":"{}"}}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}],
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	msgs := upstreamMsgs(t, f.got)
	if len(msgs) == 0 || msgs[0]["role"] != "system" {
		t.Fatalf("no system message: %v", msgs)
	}
	sys, _ := msgs[0]["content"].(string)
	if !strings.Contains(sys, "JSON") || !strings.Contains(sys, `"type":"integer"`) || !strings.Contains(sys, `"type":"object"`) {
		t.Errorf("system: %s", sys)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if _, ok := up["response_format"]; ok {
		t.Errorf("no JSON field should reach the upstream: %v", up["response_format"])
	}
}

func TestGeminiUnknownModelIs404(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", geminiText)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	// 11 characters of text → 2 tokens by the estimate
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hello world"}]}]}`)
	if code != 200 || body != `{"totalTokens":2}` {
		t.Errorf("direct: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":2`) {
		t.Errorf("wrapped: %d %s", code, body)
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) ||
		!strings.Contains(body, `"supportedGenerationMethods":["generateContent","streamGenerateContent","countTokens"]`) {
		t.Errorf("%d %s", rec.Code, body)
	}
}

func TestGeminiRootAndFallbackMentionGemini(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("root info: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("fallback: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiCallIsRecorded(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent", strings.NewReader(geminiText)))
	calls := s.Recent()
	if len(calls) != 1 || calls[0].From != geminiFrom || calls[0].Provider != "fake" || calls[0].Status != 200 ||
		calls[0].Usage.Input != 10 || calls[0].Usage.Output != 5 {
		t.Errorf("recent: %+v", calls)
	}
}
