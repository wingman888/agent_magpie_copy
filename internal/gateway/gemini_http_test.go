package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

func TestGhttpGenerateContent(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"systemInstruction":{"parts":[{"text":"be brief"}]}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" || up["model"] != "m1" {
		t.Errorf("upstream %s: %s", f.path, f.got)
	}
	if msgs := up["messages"].([]any); msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages: %v", msgs)
	}
	var out struct {
		Candidates []struct {
			Content      struct{ Parts []map[string]any } `json:"content"`
			FinishReason string                           `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("bad json %v: %s", err, body)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Content.Parts[0]["text"] != "Hi" {
		t.Errorf("reply: %s", body)
	}
	if out.Candidates[0].FinishReason != "STOP" || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("finish: %s", body)
	}
	if out.UsageMetadata.PromptTokenCount != 10 || out.UsageMetadata.CandidatesTokenCount != 4 || !strings.Contains(body, `"thoughtsTokenCount"`) {
		t.Errorf("usage: %s", body)
	}
}

func TestGhttpStreamOrder(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"read a.go"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var kinds []string
	var evs []map[string]any
	for _, e := range events(body) {
		evs = append(evs, e)
		cand := e["candidates"].([]any)[0].(map[string]any)
		for _, p := range cand["content"].(map[string]any)["parts"].([]any) {
			part := p.(map[string]any)
			switch {
			case part["thought"] == true:
				kinds = append(kinds, "thought")
			case part["functionCall"] != nil:
				kinds = append(kinds, "functionCall")
			default:
				kinds = append(kinds, "text")
			}
		}
	}
	if got := strings.Join(kinds, " "); got != "thought text text functionCall" {
		t.Errorf("part order: %s", got)
	}
	last := evs[len(evs)-1]["candidates"].([]any)[0].(map[string]any)
	if last["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", last)
	}
	fc := last["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("functionCall: %v", fc)
	}
	um := evs[len(evs)-1]["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage: %v", um)
	}
	if _, ok := um["thoughtsTokenCount"]; !ok {
		t.Errorf("no thoughtsTokenCount: %v", um)
	}
}

func TestGhttpCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	text := "hello world, this is a test" // 27 characters
	direct := `{"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}`
	wrapped := `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}}`
	for _, b := range []string{direct, wrapped} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		if code != 200 || body != `{"totalTokens":6}` {
			t.Errorf("%d %s", code, body)
		}
	}
	code, body := post(t, "/v1beta/models/nope:countTokens", direct)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
}

func TestGhttpModelList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) ||
		!strings.Contains(body, `"supportedGenerationMethods"`) || !strings.Contains(body, `"countTokens"`) {
		t.Errorf("%d %s", rec.Code, body)
	}
}

func TestGhttpUnknownModelAndMethod(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, path := range []string{"/v1beta/models/nope:generateContent", "/v1beta/models/fake/m1:bogus"} {
		code, body := post(t, path, `{"contents":[]}`)
		if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

func TestGhttpUpstreamErrorIsGemini(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance","type":"x"}}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 402 || !strings.Contains(body, `"error":{"code":402`) || !strings.Contains(body, "Fake: Insufficient Balance") || !strings.Contains(body, `"status":"UNKNOWN"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGhttpInfoAndNotFoundMentionGemini(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("info: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGhttpCallIsRecorded(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
	calls := s.Recent()
	if len(calls) != 1 {
		t.Fatalf("calls: %d", len(calls))
	}
	c := calls[0]
	if c.From != "gemini" || c.To != provider.Chat || c.Model != "fake/m1" || c.Provider != "fake" ||
		c.Status != 200 || c.Usage.Input != 10 || c.Usage.Output != 4 {
		t.Errorf("call: %+v", c)
	}
}
