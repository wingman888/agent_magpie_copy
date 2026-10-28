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

// fake is an upstream that records what it got and replies with a script.
type fake struct {
	t     *testing.T
	got   []byte
	path  string
	head  http.Header
	reply string // SSE body
	ctype string
	code  int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// setup points dial's provider file at a temp dir and adds one provider
// speaking only the given protocol, backed by the fake.
func setup(t *testing.T, proto provider.Protocol, f *fake) *httptest.Server {
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

func post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func events(body string) []map[string]any {
	var out []map[string]any
	readSSE(strings.NewReader(body), func(_, data string) error {
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			out = append(out, m)
		}
		return nil
	})
	return out
}

func TestAnthropicClientChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":100,"stream":true,"system":"be brief",
	  "messages":[{"role":"user","content":"read a.go"},
	    {"role":"assistant","content":[{"type":"tool_use","id":"t0","name":"read","input":{"path":"x"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"t0","content":"package x"}]}],
	  "tools":[{"name":"read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
	  "thinking":{"type":"enabled","budget_tokens":5000}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	// what the upstream saw
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/chat/completions" || f.head.Get("Authorization") != "Bearer k" {
		t.Errorf("upstream path/auth: %s %v", f.path, f.head)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" {
		t.Errorf("messages: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" || up["stream"] != true || up["max_tokens"] != float64(100) {
		t.Errorf("params: %v", up)
	}
	if _, ok := up["tools"].([]any)[0].(map[string]any)["function"]; !ok {
		t.Errorf("tools: %v", up["tools"])
	}
	// what the client got
	evs := events(body)
	var types []string
	for _, e := range evs {
		types = append(types, e["type"].(string))
	}
	want := "message_start content_block_start content_block_delta content_block_stop content_block_start content_block_delta content_block_delta content_block_stop content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop"
	if got := strings.Join(types, " "); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if b := evs[1]["content_block"].(map[string]any); b["type"] != "thinking" {
		t.Errorf("first block: %v", b)
	}
	if b := evs[8]["content_block"].(map[string]any); b["type"] != "tool_use" || b["name"] != "read" || b["id"] != "call_1" {
		t.Errorf("tool block: %v", b)
	}
	md := evs[len(evs)-2]
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" || md["usage"].(map[string]any)["output_tokens"] != float64(5) {
		t.Errorf("message_delta: %v", md)
	}
}

func TestChatClientAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"ls","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"dir\":\".\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	// non-streaming client
	code, body := post(t, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"system","content":"sys"},{"role":"user","content":"ls"}],
	  "tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}],"temperature":0.2,"max_tokens":50}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["system"] != "sys" || up["max_tokens"] != float64(50) || up["temperature"] != 0.2 || up["stream"] != true {
		t.Errorf("upstream: %s", f.got)
	}
	if f.head.Get("x-api-key") != "k" || f.head.Get("anthropic-version") == "" {
		t.Errorf("headers: %v", f.head)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}
	}
	json.Unmarshal([]byte(body), &out)
	c := out.Choices[0]
	if c.Message.Content != "hello" || c.FinishReason != "tool_calls" || len(c.Message.ToolCalls) != 1 ||
		c.Message.ToolCalls[0].Function.Arguments != `{"dir":"."}` || c.Message.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("reply: %s", body)
	}
	if out.Usage.PromptTokens != 10 || out.Usage.CompletionTokens != 4 {
		t.Errorf("usage: %s", body)
	}
}

func TestResponsesClientChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/responses", `{"model":"m1","stream":true,"instructions":"you are codex",
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"list"}]},
	    {"type":"function_call","call_id":"c0","name":"shell","arguments":"{\"cmd\":\"pwd\"}"},
	    {"type":"function_call_output","call_id":"c0","output":"/tmp"}],
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"reasoning":{"effort":"high"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[2].(map[string]any)["tool_calls"] == nil || msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("upstream messages: %v", msgs)
	}
	if up["reasoning_effort"] != "high" {
		t.Errorf("effort: %v", up["reasoning_effort"])
	}
	var types []string
	var done []map[string]any
	for _, e := range events(body) {
		types = append(types, e["type"].(string))
		if e["type"] == "response.output_item.done" {
			done = append(done, e["item"].(map[string]any))
		}
	}
	want := "response.created response.in_progress response.output_item.added response.content_part.added response.output_text.delta response.output_text.done response.content_part.done response.output_item.done response.output_item.added response.function_call_arguments.delta response.function_call_arguments.done response.output_item.done response.completed"
	if got := strings.Join(types, " "); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
	if len(done) != 2 || done[1]["type"] != "function_call" || done[1]["call_id"] != "call_9" || done[1]["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("items: %v", done)
	}
}

func TestPassthroughRewritesModel(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	setup(t, provider.Anthropic, f)
	p, _ := provider.Find("fake")
	p.Models = []string{"real-model"}
	provider.Save(*p)
	code, body := post(t, "/v1/messages", `{"model":"fake/real-model","max_tokens":1.5e2,"messages":[],"metadata":{"x":12345678901234567890}}`)
	if code != 200 || !strings.Contains(body, `"id":"msg"`) {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`"model":"real-model"`)) || !bytes.Contains(f.got, []byte(`12345678901234567890`)) || !bytes.Contains(f.got, []byte(`1.5e2`)) {
		t.Errorf("rewritten body: %s", f.got)
	}
	if f.path != "/v1/messages" {
		t.Errorf("path %s", f.path)
	}
}

func TestErrorsAndUnknownModel(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance","type":"x"}}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/messages", `{"model":"m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 402 || !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "Fake: Insufficient Balance") {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1/chat/completions", `{"model":"nope","messages":[]}`)
	if code != 404 || !strings.Contains(body, `"error":{`) || !strings.Contains(body, "m1") {
		t.Errorf("%d %s", code, body)
	}
}

func TestModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"fake/m1"`) || !strings.Contains(rec.Body.String(), `"display_name"`) {
		t.Errorf("%s", rec.Body.String())
	}
}
