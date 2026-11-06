package gateway

// Independent black-box checks of the Gemini API, written only against the
// public entry points (New().Handler(), provider.Save) and the fake upstreams.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// zzbbFake is an upstream that records the request it got and replies with a script.
type zzbbFake struct {
	got   []byte
	path  string
	reply string
}

func (f *zzbbFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path = r.URL.Path
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, f.reply)
}

func zzbbSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// zzbbSetup isolates HOME and XDG dirs, starts the fake upstream and saves
// one provider "fake" with model m1 speaking only the given protocol.
func zzbbSetup(t *testing.T, proto provider.Protocol, f *zzbbFake) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// zzbbDo sends one request through the gateway's public handler.
func zzbbDo(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	return rec.Code, rec.Body.String()
}

func zzbbMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func zzbbList(v any) []any {
	l, _ := v.([]any)
	return l
}

func zzbbStr(v any) string {
	s, _ := v.(string)
	return s
}

func zzbbJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	return m
}

// zzbbCandidateParts returns the parts of candidates[0] in a Gemini reply.
func zzbbCandidateParts(t *testing.T, m map[string]any) []any {
	t.Helper()
	cands := zzbbList(m["candidates"])
	if len(cands) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	return zzbbList(zzbbMap(zzbbMap(cands[0])["content"])["parts"])
}

// zzbbGenReq is a Gemini request with every feature the spec lists for a
// translated call: system instruction, uppercase tool schema, a tool config
// that allows exactly one function, and thinkingBudget -1.
const zzbbGenReq = `{
 "systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[{"role":"user","parts":[{"text":"read a.go"}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
   "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
 "generationConfig":{"maxOutputTokens":100,"temperature":0.2,"thinkingConfig":{"thinkingBudget":-1}}}`

const zzbbGenPath = "/v1beta/models/fake/m1:generateContent"

// a) Chat upstream: text, finishReason, usage; upstream sees system, lowercase
// schema, medium reasoning effort and the single-function tool choice.
func TestZzBBGeminiGenerateChatUpstream(t *testing.T) {
	f := &zzbbFake{reply: zzbbSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	zzbbSetup(t, provider.Chat, f)
	code, body := zzbbDo(t, "POST", zzbbGenPath, zzbbGenReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// client side
	m := zzbbJSON(t, body)
	parts := zzbbCandidateParts(t, m)
	if len(parts) == 0 || zzbbStr(zzbbMap(parts[0])["text"]) != "ok" {
		t.Errorf("text part missing: %s", body)
	}
	cand := zzbbMap(zzbbList(m["candidates"])[0])
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}
	usage := zzbbMap(m["usageMetadata"])
	if usage == nil {
		t.Fatalf("no usageMetadata: %s", body)
	}
	if usage["promptTokenCount"] != float64(3) || usage["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usageMetadata = %v", usage)
	}

	// upstream side
	up := zzbbJSON(t, string(f.got))
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	msgs := zzbbList(up["messages"])
	if len(msgs) == 0 || zzbbMap(msgs[0])["role"] != "system" || !strings.Contains(zzbbStr(zzbbMap(msgs[0])["content"]), "be brief") {
		t.Errorf("system message missing or wrong: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", up["reasoning_effort"])
	}
	tools := zzbbList(up["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools = %v", up["tools"])
	}
	params := zzbbMap(zzbbMap(zzbbMap(tools[0])["function"])["parameters"])
	if params["type"] != "object" {
		t.Errorf("tool schema type = %v, want lowercase object", params["type"])
	}
	if zzbbMap(zzbbMap(params["properties"])["path"])["type"] != "string" {
		t.Errorf("property type not lowercased: %v", params)
	}
	tc := zzbbMap(up["tool_choice"])
	if tc["type"] != "function" || zzbbMap(tc["function"])["name"] != "read" {
		t.Errorf("tool_choice = %v, want the single allowed function", up["tool_choice"])
	}
}

// a) Anthropic upstream: system goes to the system field; medium thinking is a
// budget between the low and high bands.
func TestZzBBGeminiGenerateAnthropicUpstream(t *testing.T) {
	f := &zzbbFake{reply: zzbbSSE(
		`data: {"type":"message_start","message":{"id":"msg1","model":"m1","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	zzbbSetup(t, provider.Anthropic, f)
	code, body := zzbbDo(t, "POST", zzbbGenPath, zzbbGenReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	if zzbbMap(zzbbCandidateParts(t, m)[0])["text"] != "ok" || zzbbMap(zzbbList(m["candidates"])[0])["finishReason"] != "STOP" {
		t.Errorf("reply: %s", body)
	}
	if zzbbMap(m["usageMetadata"]) == nil {
		t.Errorf("no usageMetadata: %s", body)
	}
	up := zzbbJSON(t, string(f.got))
	if f.path != "/v1/messages" {
		t.Errorf("upstream path %s", f.path)
	}
	if !strings.Contains(zzbbStr(up["system"]), "be brief") {
		t.Errorf("system field = %v", up["system"])
	}
	th := zzbbMap(up["thinking"])
	budget, _ := th["budget_tokens"].(float64)
	if th["type"] != "enabled" || budget <= 4096 || budget > 12000 {
		t.Errorf("thinking = %v, want medium budget", up["thinking"])
	}
	tools := zzbbList(up["tools"])
	if len(tools) != 1 || zzbbMap(zzbbMap(tools[0])["input_schema"])["type"] != "object" {
		t.Errorf("tools = %v", up["tools"])
	}
}

// a) Responses upstream: reasoning effort medium.
func TestZzBBGeminiGenerateResponsesUpstream(t *testing.T) {
	f := &zzbbFake{reply: zzbbSSE(
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","model":"m1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`)}
	zzbbSetup(t, provider.Responses, f)
	code, body := zzbbDo(t, "POST", zzbbGenPath, zzbbGenReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	if zzbbMap(zzbbCandidateParts(t, m)[0])["text"] != "ok" || zzbbMap(zzbbList(m["candidates"])[0])["finishReason"] != "STOP" {
		t.Errorf("reply: %s", body)
	}
	if zzbbMap(m["usageMetadata"]) == nil {
		t.Errorf("no usageMetadata: %s", body)
	}
	up := zzbbJSON(t, string(f.got))
	if zzbbMap(up["reasoning"])["effort"] != "medium" {
		t.Errorf("reasoning = %v, want effort medium", up["reasoning"])
	}
}

// b) Streaming: thought, text, functionCall in order; the last event carries
// finishReason STOP and usage with the upstream reasoning count.
func TestZzBBGeminiStreamSSE(t *testing.T) {
	f := &zzbbFake{reply: zzbbSSE(
		`data: {"id":"c9","model":"m1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`data: {"id":"c9","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c9","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c9","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c9","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c9","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c9","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":4}}}`,
		`data: [DONE]`)}
	zzbbSetup(t, provider.Chat, f)
	code, body := zzbbDo(t, "POST", "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", zzbbGenReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var evs []map[string]any
	readSSE(strings.NewReader(body), func(_, data string) error {
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			evs = append(evs, m)
		}
		return nil
	})
	if len(evs) == 0 {
		t.Fatalf("no SSE events: %s", body)
	}
	// flatten the parts in order
	var kinds []string
	var fc map[string]any
	for _, ev := range evs {
		cands := zzbbList(ev["candidates"])
		if len(cands) == 0 {
			continue
		}
		for _, p := range zzbbList(zzbbMap(zzbbMap(cands[0])["content"])["parts"]) {
			pm := zzbbMap(p)
			switch {
			case pm["thought"] == true:
				kinds = append(kinds, "thought")
			case pm["functionCall"] != nil:
				kinds = append(kinds, "functionCall")
				fc = zzbbMap(pm["functionCall"])
			case pm["text"] != nil:
				kinds = append(kinds, "text")
			}
		}
	}
	if got := strings.Join(kinds, " "); got != "thought text functionCall" {
		t.Errorf("part order = %q, want thought text functionCall", got)
	}
	if fc == nil || fc["id"] != "call_7" || fc["name"] != "read" || zzbbMap(fc["args"])["path"] != "a.go" {
		t.Errorf("functionCall = %v", fc)
	}
	last := evs[len(evs)-1]
	lc := zzbbList(last["candidates"])
	if len(lc) == 0 || zzbbMap(lc[0])["finishReason"] != "STOP" {
		t.Errorf("last event finishReason: %v", last)
	}
	usage := zzbbMap(last["usageMetadata"])
	if usage == nil || usage["thoughtsTokenCount"] != float64(4) {
		t.Errorf("last event usage = %v, want thoughtsTokenCount 4", last["usageMetadata"])
	}
}

// c) unknown model -> 404, compact body with NOT_FOUND.
func TestZzBBGeminiUnknownModel404(t *testing.T) {
	zzbbSetup(t, provider.Chat, &zzbbFake{})
	code, body := zzbbDo(t, "POST", "/v1beta/models/nosuch/x:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 404 {
		t.Fatalf("status %d, want 404: %s", code, body)
	}
	if !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("body lacks compact NOT_FOUND: %s", body)
	}
}

// d) countTokens takes both request shapes and answers totalTokens.
func TestZzBBGeminiCountTokensBothShapes(t *testing.T) {
	zzbbSetup(t, provider.Chat, &zzbbFake{})
	direct := `{"contents":[{"role":"user","parts":[{"text":"hello world, counting tokens"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world, counting tokens"}]}]}}`
	for name, b := range map[string]string{"direct": direct, "wrapped": wrapped} {
		code, body := zzbbDo(t, "POST", "/v1beta/models/fake/m1:countTokens", b)
		if code != 200 {
			t.Errorf("%s: status %d: %s", name, code, body)
			continue
		}
		m := zzbbJSON(t, body)
		if _, ok := m["totalTokens"].(float64); !ok {
			t.Errorf("%s: no totalTokens: %s", name, body)
		}
		if !strings.Contains(body, `"totalTokens":`) {
			t.Errorf("%s: not compact: %s", name, body)
		}
	}
}

// e) GET /v1beta/models lists the catalog with models/<id> names.
func TestZzBBGeminiListModels(t *testing.T) {
	zzbbSetup(t, provider.Chat, &zzbbFake{})
	code, body := zzbbDo(t, "GET", "/v1beta/models", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	found := false
	for _, x := range zzbbList(m["models"]) {
		e := zzbbMap(x)
		if e["name"] == "models/fake/m1" {
			found = true
			if len(zzbbList(e["supportedGenerationMethods"])) == 0 {
				t.Errorf("no supportedGenerationMethods: %v", e)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

// f) root description and unknown-path 404 both mention v1beta.
func TestZzBBGeminiRootAndNotFoundMentionV1beta(t *testing.T) {
	zzbbSetup(t, provider.Chat, &zzbbFake{})
	if code, body := zzbbDo(t, "GET", "/", ""); code != 200 || !strings.Contains(body, "v1beta") {
		t.Errorf("root: %d %s", code, body)
	}
	if code, body := zzbbDo(t, "GET", "/no/such/path", ""); code != 404 || !strings.Contains(body, "v1beta") {
		t.Errorf("unknown path: %d %s", code, body)
	}
}
