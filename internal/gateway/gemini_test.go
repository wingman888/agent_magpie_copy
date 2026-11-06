package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// partsOfEvent returns the parts of the first candidate of a Gemini event.
func partsOfEvent(t *testing.T, ev map[string]any) []map[string]any {
	t.Helper()
	cands, ok := ev["candidates"].([]any)
	if !ok || len(cands) == 0 {
		t.Fatalf("no candidates: %v", ev)
	}
	content, _ := cands[0].(map[string]any)["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"id":"t0","name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`)
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
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" {
		t.Fatalf("messages: %v", msgs)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "t0" {
		t.Errorf("tool result id: %v", msgs[3])
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	if calls[0].(map[string]any)["id"] != "t0" {
		t.Errorf("assistant call id: %v", calls)
	}
	if up["max_tokens"] != float64(100) || up["reasoning_effort"] != "medium" || up["stream"] != true {
		t.Errorf("params: %s", f.got)
	}

	// what the client got
	evs := events(body)
	var thoughts, texts, calls2 int
	var fc map[string]any
	for _, ev := range evs {
		for _, p := range partsOfEvent(t, ev) {
			switch {
			case p["thought"] == true && p["text"] == "hmm":
				thoughts++
			case p["functionCall"] != nil:
				calls2++
				fc = p["functionCall"].(map[string]any)
			case p["text"] != nil && p["thought"] == nil:
				texts++
			}
		}
	}
	if thoughts != 1 || texts != 2 || calls2 != 1 {
		t.Fatalf("parts: thoughts %d texts %d calls %d in %s", thoughts, texts, calls2, body)
	}
	if fc["id"] != "call_1" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call: %v", fc)
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finish: %v", cand)
	}
	um, _ := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateContentChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"list"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["stream"] != true {
		t.Errorf("upstream should be streamed: %s", f.got)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string           `json:"role"`
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
		ResponseID   string `json:"responseId"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].FinishReason != "STOP" || out.Candidates[0].Content.Role != "model" {
		t.Fatalf("candidate: %s", body)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 2 || parts[0]["text"] != "ok" {
		t.Fatalf("parts: %s", body)
	}
	fc, _ := parts[1]["functionCall"].(map[string]any)
	if fc == nil || fc["name"] != "shell" || fc["id"] != "call_9" || fc["args"].(map[string]any)["cmd"] != "ls" {
		t.Errorf("functionCall: %v", parts[1])
	}
	if out.UsageMetadata.PromptTokenCount != 3 || out.UsageMetadata.CandidatesTokenCount != 2 || out.UsageMetadata.TotalTokenCount != 5 {
		t.Errorf("usage: %+v", out.UsageMetadata)
	}
	if out.ResponseID == "" {
		t.Errorf("no responseId: %s", body)
	}
}

func TestGeminiSchemaImageJSONAndToolChoice(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","model":"m1","choices":[{"delta":{"content":"{}"}}]}`,
		`data: {"id":"c3","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[
	    {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	    {"inlineData":{"mimeType":"application/pdf","data":"BBBB"}},
	    {"text":"describe"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT",
	    "properties":{"tags":{"type":"ARRAY","items":{"type":"STRING"}},"n":{"type":"INTEGER"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":77,"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"items":{"type":"ARRAY","items":{"type":"INTEGER"}}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["max_tokens"] != float64(77) {
		t.Errorf("max_tokens: %v", up["max_tokens"])
	}
	s := string(f.got)
	if !strings.Contains(s, `data:image/png;base64,AAAA`) || !strings.Contains(s, `[attachment of type application/pdf omitted]`) {
		t.Errorf("content: %s", s)
	}
	msgs := up["messages"].([]any)
	if sys, _ := msgs[0].(map[string]any); sys["role"] != "system" || !strings.Contains(sys["content"].(string), "JSON Schema") {
		t.Errorf("json instruction missing: %v", msgs[0])
	}
	tools := up["tools"].([]any)
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	tags := props["tags"].(map[string]any)
	if params["type"] != "object" || props["n"].(map[string]any)["type"] != "integer" ||
		tags["type"] != "array" || tags["items"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	tc, _ := up["tool_choice"].(map[string]any)
	if tc == nil || tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}

func TestGeminiUnknownModel404(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	if code != 404 || out.Error.Code != 404 || out.Error.Status != "NOT_FOUND" || out.Error.Message == "" {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokensAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hello world!!"}]}]}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":3`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world!!"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":3`) {
		t.Errorf("wrapped countTokens: %d %s", code, body)
	}

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var list struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("models not JSON: %s", rec.Body.String())
	}
	found := false
	for _, m := range list.Models {
		if m.Name == "models/fake/m1" {
			found = true
			if m.DisplayName == "" || len(m.SupportedGenerationMethods) != 3 {
				t.Errorf("model: %+v", m)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 missing: %s", rec.Body.String())
	}
}

func TestGeminiRootInfoListsAPIs(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "/v1beta/models/{model}:streamGenerateContent") {
		t.Errorf("root info: %s", rec.Body.String())
	}
}
