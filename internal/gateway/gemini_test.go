package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiBody is a generateContent request in the Gemini API's shape, with a
// system instruction, a function call and its result (matched by name), and
// a tool declaration.
const geminiBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"c0","name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}],
  "generationConfig":{"maxOutputTokens":100}}`

// geminiBodyThoughts is geminiBody asking for its reasoning to be returned.
var geminiBodyThoughts = strings.Replace(geminiBody,
	`"generationConfig":{"maxOutputTokens":100}`,
	`"generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"includeThoughts":true}}`, 1)

// candParts returns the parts of a Gemini chunk's first candidate.
func candParts(t *testing.T, chunk map[string]any) []map[string]any {
	t.Helper()
	cands, _ := chunk["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatalf("no candidates in %v", chunk)
	}
	cand, _ := cands[0].(map[string]any)
	content, _ := cand["content"].(map[string]any)
	ps, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range ps {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
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
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	cat := provider.Catalog()
	if len(cat) == 0 || len(out.Models) != len(cat) {
		t.Fatalf("%d models listed, catalog has %d", len(out.Models), len(cat))
	}
	names := map[string]bool{}
	for _, m := range out.Models {
		names[m.Name] = true
		if m.DisplayName == "" {
			t.Errorf("no displayName for %s", m.Name)
		}
		if strings.Join(m.SupportedGenerationMethods, ",") != "generateContent,streamGenerateContent,countTokens" {
			t.Errorf("%s methods: %v", m.Name, m.SupportedGenerationMethods)
		}
	}
	for _, e := range cat {
		if !names["models/"+e.ID] {
			t.Errorf("models/%s not listed", e.ID)
		}
	}
	if !names["models/fake/m1"] {
		t.Errorf("models/fake/m1 not listed: %s", rec.Body.String())
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	// 12 bytes of text: about 3 tokens
	direct := `{"contents":[{"role":"user","parts":[{"text":"abcdefghijkl"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"abcdefghijkl"}]}]}}`
	for _, body := range []string{direct, wrapped} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		if code != 200 || !strings.Contains(out, `"totalTokens":3`) {
			t.Errorf("%d %s for %s", code, out, body)
		}
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, path := range []string{"/v1beta/models/nope:generateContent", "/v1beta/models/nope:countTokens",
		"/v1beta/models/nope:streamGenerateContent?alt=sse"} {
		code, body := post(t, path, geminiBody)
		var e struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		json.Unmarshal([]byte(body), &e)
		if code != 404 || e.Error.Code != 404 || e.Error.Status != "NOT_FOUND" || !strings.Contains(e.Error.Message, "nope") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

func TestGeminiRootAndNotFoundHints(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	for _, want := range []string{"/v1beta/models", "{model}:generateContent", "{model}:streamGenerateContent", "{model}:countTokens"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("root info lacks %q: %s", want, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "/v1beta/models") {
		t.Errorf("404 hint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiStreamNeedsAltSSE(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent", geminiBody)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiGenerateContentChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	// what the upstream saw: the vendor's model id, the Chat shape, and the
	// function result matched to the call that had no result id
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs, _ := up["messages"].([]any)
	if f.path != "/v1/chat/completions" || up["model"] != "m1" || up["max_tokens"] != float64(100) || len(msgs) != 4 {
		t.Fatalf("upstream: %s", f.got)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("tool result not matched: %v", msgs[3])
	}
	// what the client got
	var out struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata map[string]any `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].FinishReason != "STOP" {
		t.Errorf("candidates: %s", body)
	}
	var full struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	json.Unmarshal([]byte(body), &full)
	if got := full.Candidates[0].Content.Parts; len(got) != 1 || got[0]["text"] != "Hi there" {
		t.Errorf("parts: %s", body)
	}
	u := out.UsageMetadata
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiStreamFunctionCall(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBodyThoughts)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 5 {
		t.Fatalf("%d events: %s", len(evs), body)
	}
	if p := candParts(t, evs[0]); len(p) != 1 || p[0]["thought"] != true || p[0]["text"] != "hmm" {
		t.Errorf("thought: %v", evs[0])
	}
	if p := candParts(t, evs[3]); len(p) != 1 {
		t.Fatalf("call event: %v", evs[3])
	} else {
		fc, _ := p[0]["functionCall"].(map[string]any)
		args, _ := fc["args"].(map[string]any)
		if fc["name"] != "read" || fc["id"] != "call_1" || args["path"] != "a.go" {
			t.Errorf("functionCall: %v", fc)
		}
	}
	last := evs[len(evs)-1]
	cands, _ := last["candidates"].([]any)
	fin, _ := cands[0].(map[string]any)["finishReason"]
	u, _ := last["usageMetadata"].(map[string]any)
	if fin != "STOP" || u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) {
		t.Errorf("last event: %v", last)
	}
}
