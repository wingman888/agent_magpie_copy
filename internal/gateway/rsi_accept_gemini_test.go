package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

type gfake struct {
	mu    sync.Mutex
	got   []byte
	reply string
}

func (f *gfake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = b
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, f.reply)
}

func gsse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

func gsetup(t *testing.T, f *gfake) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	return gw
}

func gpost(t *testing.T, gw *httptest.Server, path, body string) (int, string) {
	t.Helper()
	res, err := http.Post(gw.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func gevents(body string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

const geminiReq = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
  {"role":"model","parts":[{"functionCall":{"id":"fc0","name":"read_file","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"id":"fc0","name":"read_file","response":{"output":"package x"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
 "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func chatReply() string {
	return gsse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Sure"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)
}

func TestAcceptGeminiStreamOverChat(t *testing.T) {
	f := &gfake{reply: chatReply()}
	gw := gsetup(t, f)
	code, body := gpost(t, gw, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := gevents(body)
	if len(evs) == 0 {
		t.Fatalf("no events: %s", body)
	}
	var parts []map[string]any
	finish := ""
	for _, e := range evs {
		cs, _ := e["candidates"].([]any)
		if len(cs) == 0 {
			continue
		}
		c := cs[0].(map[string]any)
		if ct, ok := c["content"].(map[string]any); ok {
			ps, _ := ct["parts"].([]any)
			for _, p := range ps {
				parts = append(parts, p.(map[string]any))
			}
		}
		if fr, ok := c["finishReason"].(string); ok {
			finish = fr
		}
	}
	if len(parts) != 3 || parts[0]["thought"] != true || parts[1]["text"] != "Sure" {
		t.Fatalf("parts: %v", parts)
	}
	fc, _ := parts[2]["functionCall"].(map[string]any)
	if fc == nil || fc["name"] != "read_file" || fc["id"] != "call_1" {
		t.Fatalf("function call: %v", parts[2])
	}
	if args, _ := fc["args"].(map[string]any); args["path"] != "a.go" {
		t.Errorf("args: %v", fc)
	}
	um, _ := evs[len(evs)-1]["usageMetadata"].(map[string]any)
	if finish != "STOP" || um == nil || um["promptTokenCount"] != float64(10) ||
		um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("finish/usage: %s %v", finish, um)
	}
}

func TestAcceptGeminiUpstreamTranslation(t *testing.T) {
	f := &gfake{reply: chatReply()}
	gw := gsetup(t, f)
	code, body := gpost(t, gw, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatal(err)
	}
	if up["model"] != "m1" || up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) {
		t.Errorf("upstream: %s", f.got)
	}
	msgs, _ := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "be brief" {
		t.Errorf("system: %v", m)
	}
	if m := msgs[3].(map[string]any); m["role"] != "tool" || m["tool_call_id"] != "fc0" {
		t.Errorf("tool result: %v", m)
	}
	tools, _ := up["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %v", up["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	pj, _ := json.Marshal(fn["parameters"])
	if !strings.Contains(string(pj), `"type":"object"`) || !strings.Contains(string(pj), `"type":"string"`) ||
		strings.Contains(string(pj), "OBJECT") || strings.Contains(string(pj), "STRING") {
		t.Errorf("schema not lowercased: %s", pj)
	}
}

func TestAcceptGeminiGenerateContent(t *testing.T) {
	f := &gfake{reply: chatReply()}
	gw := gsetup(t, f)
	code, body := gpost(t, gw, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not json: %s", body)
	}
	c := m["candidates"].([]any)[0].(map[string]any)
	if c["finishReason"] != "STOP" {
		t.Errorf("finish: %v", c["finishReason"])
	}
	if !strings.Contains(body, `"functionCall"`) || !strings.Contains(body, `"read_file"`) || !strings.Contains(body, `"a.go"`) {
		t.Errorf("body: %s", body)
	}
	um, _ := m["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
	// upstream was asked non-streaming-or-streaming; either way a mid-stream tool call must be whole
}

func TestAcceptGeminiErrorsCountAndModels(t *testing.T) {
	f := &gfake{reply: chatReply()}
	gw := gsetup(t, f)
	code, body := gpost(t, gw, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 {
		t.Errorf("unknown model: %d %s", code, body)
	}
	var e struct {
		Error struct {
			Code   int    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	json.Unmarshal([]byte(body), &e)
	if e.Error.Status != "NOT_FOUND" || e.Error.Code != 404 {
		t.Errorf("error shape: %s", body)
	}

	code, body = gpost(t, gw, "/v1beta/models/fake/m1:countTokens", geminiReq)
	var ct struct {
		Total *float64 `json:"totalTokens"`
	}
	json.Unmarshal([]byte(body), &ct)
	if code != 200 || ct.Total == nil {
		t.Errorf("countTokens: %d %s", code, body)
	}
	code, body = gpost(t, gw, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world"}]}]}}`)
	ct.Total = nil
	json.Unmarshal([]byte(body), &ct)
	if code != 200 || ct.Total == nil {
		t.Errorf("countTokens wrapped: %d %s", code, body)
	}

	res, err := http.Get(gw.URL + "/v1beta/models")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var ml struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &ml); err != nil || res.StatusCode != 200 {
		t.Fatalf("models: %d %s", res.StatusCode, b)
	}
	found := false
	for _, m := range ml.Models {
		if m.Name == "models/fake/m1" {
			found = true
		}
	}
	if !found {
		t.Errorf("models: %s", b)
	}
	if !strings.Contains(string(b), "generateContent") || !strings.Contains(string(b), "streamGenerateContent") || !strings.Contains(string(b), "countTokens") {
		t.Errorf("supported methods missing: %s", b)
	}
}

func TestAcceptGeminiInfoListsEndpoint(t *testing.T) {
	gw := gsetup(t, &gfake{reply: chatReply()})
	res, err := http.Get(gw.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "v1beta") {
		t.Errorf("root info lacks gemini endpoint: %s", b)
	}
}

func TestAcceptGeminiJSONOutputAndToolConfig(t *testing.T) {
	f := &gfake{reply: gsse(
		`data: {"id":"c1","choices":[{"delta":{"content":"{}"},"finish_reason":"length"}]}`,
		`data: [DONE]`)}
	gw := gsetup(t, f)
	req := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	 "tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}]}],
	 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},
	 "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"x":{"type":"INTEGER"}}}}}`
	code, body := gpost(t, gw, "/v1beta/models/fake/m1:generateContent", req)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"MAX_TOKENS"`) {
		t.Errorf("length should map to MAX_TOKENS: %s", body)
	}
	up := string(f.got)
	if strings.Contains(up, `"INTEGER"`) || strings.Contains(up, `"OBJECT"`) {
		t.Errorf("uppercase types leaked upstream: %s", up)
	}
	var m map[string]any
	json.Unmarshal(f.got, &m)
	if m["tool_choice"] == nil {
		t.Errorf("tool choice not forwarded: %s", up)
	}
	if !strings.Contains(strings.ToLower(up), "json") {
		t.Errorf("no JSON constraint upstream: %s", up)
	}
}
