package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemhttpFake is a self-contained upstream stub: it records what it got and
// replies with a script. Copied here so this file does not depend on other
// test files in the package.
type gemhttpFake struct {
	t     *testing.T
	got   []byte
	path  string
	head  http.Header
	reply string // SSE body
	ctype string
	code  int
}

func (f *gemhttpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header
	ct := f.ctype
	if ct == "" {
		ct = "text/event-stream"
	}
	w.Header().Set("Content-Type", ct)
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

func gemhttpSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// gemhttpSetup points dial's provider file at a temp dir and adds one provider
// speaking only the given protocol, backed by the stub upstream.
func gemhttpSetup(t *testing.T, proto provider.Protocol, f *gemhttpFake) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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
	return up
}

// gemhttpPost sends a POST through a fresh gateway.
func gemhttpPost(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// gemhttpReply is an upstream Chat stream with reasoning, text and a whole
// tool call, ending in usage of 10 prompt, 5 completion of which 2 reasoning.
var gemhttpReply = gemhttpSSE(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`)

// gemhttpBody is a Gemini request with a system instruction, a function call
// and its result in history, an uppercase tool schema and a thinking budget.
const gemhttpBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[
   {"role":"user","parts":[{"text":"read a.go"}]},
   {"role":"model","parts":[{"functionCall":{"id":"t0","name":"read","args":{"path":"x"}}}]},
   {"role":"user","parts":[{"functionResponse":{"id":"t0","name":"read","response":{"output":"package x"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
   "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
 "generationConfig":{"maxOutputTokens":300,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

// gemhttpEvents reads the JSON objects of an SSE body.
func gemhttpEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(block), "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		out = append(out, m)
	}
	return out
}

// gemhttpParts returns the parts of the first candidate of a Gemini object.
func gemhttpParts(m map[string]any) []map[string]any {
	cands, _ := m["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	content, _ := cands[0].(map[string]any)["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

// gemhttpGet serves a GET through a fresh gateway.
func gemhttpGet(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// gemhttpErr reads the Gemini error object of a reply.
func gemhttpErr(t *testing.T, body string) map[string]any {
	t.Helper()
	var m struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil || m.Error == nil {
		t.Fatalf("not a Gemini error: %s", body)
	}
	return m.Error
}

func TestGemHTTPStreamSSE(t *testing.T) {
	f := &gemhttpFake{t: t, reply: gemhttpReply}
	gemhttpSetup(t, provider.Chat, f)
	code, body := gemhttpPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", gemhttpBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream got: lowercase schema, medium effort, max tokens
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["max_tokens"] != float64(300) || up["reasoning_effort"] != "medium" {
		t.Errorf("upstream params: %s", f.got)
	}
	if bytes.Contains(f.got, []byte(`"OBJECT"`)) || bytes.Contains(f.got, []byte(`"STRING"`)) ||
		!bytes.Contains(f.got, []byte(`"type":"object"`)) || !bytes.Contains(f.got, []byte(`"type":"string"`)) {
		t.Errorf("schema not lowercased: %s", f.got)
	}

	// what the client got
	evs := gemhttpEvents(t, body)
	if len(evs) < 5 {
		t.Fatalf("too few events: %s", body)
	}
	var texts []string
	var calls []map[string]any
	for _, e := range evs[:len(evs)-1] {
		for _, p := range gemhttpParts(e) {
			switch {
			case p["thought"] == true:
				texts = append(texts, "THINK:"+p["text"].(string))
			case p["functionCall"] != nil:
				calls = append(calls, p["functionCall"].(map[string]any))
			case p["text"] != nil:
				texts = append(texts, p["text"].(string))
			}
		}
	}
	if got := strings.Join(texts, "|"); got != "THINK:hmm|Hi |there" {
		t.Errorf("streamed parts: %q", got)
	}
	if len(calls) != 1 || calls[0]["id"] != "call_1" || calls[0]["name"] != "read" {
		t.Fatalf("calls: %v", calls)
	}
	if args, _ := calls[0]["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args not whole: %v", calls[0]["args"])
	}
	last := evs[len(evs)-1]
	cand := last["candidates"].([]any)[0].(map[string]any)
	um, _ := last["usageMetadata"].(map[string]any)
	if cand["finishReason"] != "STOP" || um["promptTokenCount"] != float64(10) ||
		um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("final event: %s", body)
	}
}

func TestGemHTTPGenerateContentJSON(t *testing.T) {
	f := &gemhttpFake{t: t, reply: gemhttpReply}
	gemhttpSetup(t, provider.Chat, f)
	code, body := gemhttpPost(t, "/v1beta/models/fake/m1:generateContent", gemhttpBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	cand := out["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason: %s", body)
	}
	var text string
	var call map[string]any
	for _, p := range gemhttpParts(out) {
		if p["text"] != nil && p["thought"] != true {
			text += p["text"].(string)
		}
		if p["functionCall"] != nil {
			call = p["functionCall"].(map[string]any)
		}
	}
	if text != "Hi there" {
		t.Errorf("text %q", text)
	}
	if call == nil || call["id"] != "call_1" || call["name"] != "read" {
		t.Fatalf("functionCall: %s", body)
	}
	if args, _ := call["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args: %v", call["args"])
	}
	um, _ := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
}

func TestGemHTTPStreamNeedsSSE(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t, reply: gemhttpReply})
	code, body := gemhttpPost(t, "/v1beta/models/fake/m1:streamGenerateContent", gemhttpBody)
	if code != 400 || gemhttpErr(t, body)["status"] != "INVALID_ARGUMENT" {
		t.Errorf("%d %s", code, body)
	}
}

func TestGemHTTPUpstreamErrorIsGeminiShaped(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance"}}`})
	code, body := gemhttpPost(t, "/v1beta/models/fake/m1:generateContent", gemhttpBody)
	e := gemhttpErr(t, body)
	if code != 402 || e["code"] != float64(402) || !strings.Contains(e["message"].(string), "Insufficient Balance") {
		t.Errorf("%d %s", code, body)
	}
}

func TestGemHTTPUnknownModel404(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t})
	code, body := gemhttpPost(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	e := gemhttpErr(t, body)
	if code != 404 || e["code"] != float64(404) || e["status"] != "NOT_FOUND" || !strings.Contains(e["message"].(string), "nope") {
		t.Errorf("%d %s", code, body)
	}
	code, body = gemhttpPost(t, "/v1beta/models/nope:countTokens", `{"contents":[]}`)
	if code != 404 || gemhttpErr(t, body)["status"] != "NOT_FOUND" {
		t.Errorf("countTokens: %d %s", code, body)
	}
}

func TestGemHTTPCountTokensBothShapes(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t})
	direct := `{"contents":[{"role":"user","parts":[{"text":"abcdefghijkl"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"abcdefghijkl"}]}]}}`
	for _, b := range []string{direct, wrapped} {
		code, body := gemhttpPost(t, "/v1beta/models/fake/m1:countTokens", b)
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		if code != 200 || out["totalTokens"] != float64(3) {
			t.Errorf("%d %s", code, body)
		}
	}
}

func TestGemHTTPModelList(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t})
	code, body := gemhttpGet(t, "/v1beta/models")
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
		t.Fatalf("%d %s", code, body)
	}
	found := false
	for _, m := range out.Models {
		if m.Name == "models/fake/m1" {
			found = true
			if m.DisplayName == "" || len(m.SupportedGenerationMethods) != 3 {
				t.Errorf("entry: %+v", m)
			}
			for _, want := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
				if !strings.Contains(strings.Join(m.SupportedGenerationMethods, ","), want) {
					t.Errorf("missing method %s", want)
				}
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}

	code, body = gemhttpGet(t, "/v1beta/models/fake/m1")
	if code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) {
		t.Errorf("single model: %d %s", code, body)
	}
	code, body = gemhttpGet(t, "/v1beta/models/nope")
	if code != 404 || gemhttpErr(t, body)["status"] != "NOT_FOUND" {
		t.Errorf("unknown single model: %d %s", code, body)
	}
}

func TestGemHTTPRootAndHintListGemini(t *testing.T) {
	_, body := gemhttpGet(t, "/")
	if !strings.Contains(body, "/v1beta/models") || !strings.Contains(body, ":generateContent") {
		t.Errorf("info: %s", body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing-here", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "/v1beta/models") {
		t.Errorf("404 hint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGemHTTPColonInModelID(t *testing.T) {
	cases := map[string][3]string{
		"fake/a:b:streamGenerateContent": {"fake/a:b", "streamGenerateContent", "ok"},
		"fake/m1:countTokens":            {"fake/m1", "countTokens", "ok"},
		"fake/m1":                        {"", "", ""},
		":generateContent":               {"", "", ""},
		"fake/m1:":                       {"", "", ""},
	}
	for in, want := range cases {
		model, method, ok := splitGeminiPath(in)
		got := [3]string{model, method, "ok"}
		if !ok {
			got[2] = ""
		}
		if got != want {
			t.Errorf("split %q: got %v want %v", in, got, want)
		}
	}

	// end to end: the id with colons reaches the upstream intact
	f := &gemhttpFake{t: t, reply: gemhttpReply}
	gemhttpSetup(t, provider.Chat, f)
	code, body := gemhttpPost(t, "/v1beta/models/fake/a:b:generateContent", gemhttpBody)
	if code != 200 || !bytes.Contains(f.got, []byte(`"model":"a:b"`)) {
		t.Errorf("%d %s upstream %s", code, body, f.got)
	}
}

// An unknown model is a 404 even when the body is empty or not JSON, so the
// model is checked before the body is parsed.
func TestGemHTTPUnknownModelBeforeBody(t *testing.T) {
	gemhttpSetup(t, provider.Chat, &gemhttpFake{t: t})
	for _, body := range []string{"", "{not json"} {
		for _, path := range []string{
			"/v1beta/models/nope:generateContent",
			"/v1beta/models/nope:streamGenerateContent?alt=sse",
		} {
			code, rb := gemhttpPost(t, path, body)
			e := gemhttpErr(t, rb)
			if code != 404 || e["code"] != float64(404) || e["status"] != "NOT_FOUND" {
				t.Errorf("%s body %q: %d %s", path, body, code, rb)
			}
		}
	}
}
