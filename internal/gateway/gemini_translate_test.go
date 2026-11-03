package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemChatReply is a Chat upstream reply that says "ok" and ends with finish.
func gemChatReply(finish string) string {
	return sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"`+finish+`"}]}`,
		`data: [DONE]`)
}

// gemRun sets up a Chat upstream that answers with reply, posts body to path,
// and returns the fake (what the upstream saw), the status and the body.
func gemRun(t *testing.T, reply, path, body string) (*fake, int, string) {
	t.Helper()
	f := &fake{t: t, reply: reply}
	setup(t, provider.Chat, f)
	code, out := post(t, path, body)
	return f, code, out
}

// gemUpstream decodes the Chat request the upstream received.
func gemUpstream(t *testing.T, f *fake) map[string]any {
	t.Helper()
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v: %s", err, f.got)
	}
	return up
}

// gemToolNames lists the names of the tools the upstream was offered.
func gemToolNames(up map[string]any) []string {
	var names []string
	tools, _ := up["tools"].([]any)
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names = append(names, name)
	}
	return names
}

const gemGenerate = "/v1beta/models/fake/m1:generateContent"

func TestGeminiSchemaTypesLowered(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}],
	  "tools":[{"functionDeclarations":[{"name":"f","description":"d","parameters":{"type":"OBJECT",
	    "properties":{"type":{"type":"STRING"},"items":{"type":"ARRAY","items":{"type":"INTEGER"}},"n":{"type":"NUMBER"}},
	    "required":["type"]}}]}]}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	up := gemUpstream(t, f)
	tools, _ := up["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %s", f.got)
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	got, _ := json.Marshal(fn["parameters"])
	// property names are data: the property called "type" and the one called
	// "items" keep their place, and only the type names change case
	want := `{"properties":{"items":{"items":{"type":"integer"},"type":"array"},"n":{"type":"number"},"type":{"type":"string"}},"required":["type"],"type":"object"}`
	if string(got) != want {
		t.Errorf("schema:\n got %s\nwant %s", got, want)
	}
}

func TestGeminiJSONOutputInstruction(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"be terse"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"ok":{"type":"BOOLEAN"}}}}}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	up := gemUpstream(t, f)
	msgs, _ := up["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages: %s", f.got)
	}
	first, _ := msgs[0].(map[string]any)
	sys, _ := first["content"].(string)
	for _, want := range []string{"be terse", "single JSON value", `{"properties":{"ok":{"type":"boolean"}},"type":"object"}`} {
		if !strings.Contains(sys, want) {
			t.Errorf("system lacks %q: %q", want, sys)
		}
	}
}

func TestGeminiToolChoice(t *testing.T) {
	const decls = `[{"functionDeclarations":[{"name":"a","parameters":{"type":"OBJECT"}},` +
		`{"name":"b","parameters":{"type":"OBJECT"}},{"name":"c","parameters":{"type":"OBJECT"}}]}]`
	abc := []string{"a", "b", "c"}
	cases := []struct {
		name   string
		cfg    string
		choice string // the upstream's tool_choice, as JSON
		names  []string
	}{
		{"none", `{"mode":"NONE"}`, `"none"`, abc},
		{"auto", `{"mode":"AUTO"}`, `"auto"`, abc},
		{"any", `{"mode":"ANY"}`, `"required"`, abc},
		{"any one", `{"mode":"ANY","allowedFunctionNames":["b"]}`, `{"function":{"name":"b"},"type":"function"}`, []string{"b"}},
		{"any two", `{"mode":"ANY","allowedFunctionNames":["a","c"]}`, `"required"`, []string{"a", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}],"tools":` + decls +
				`,"toolConfig":{"functionCallingConfig":` + tc.cfg + `}}`
			f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			up := gemUpstream(t, f)
			choice, _ := json.Marshal(up["tool_choice"])
			if string(choice) != tc.choice {
				t.Errorf("tool_choice %s, want %s", choice, tc.choice)
			}
			names := gemToolNames(up)
			if strings.Join(names, ",") != strings.Join(tc.names, ",") {
				t.Errorf("tools %v, want %v", names, tc.names)
			}
		})
	}
}

func TestGeminiThinkingEffort(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want any // the upstream's reasoning_effort; nil for none
	}{
		{"budget -1 is medium", `{"thinkingBudget":-1,"includeThoughts":true}`, "medium"},
		{"small budget is low", `{"thinkingBudget":2000}`, "low"},
		{"budget 0 asks for none", `{"thinkingBudget":0}`, nil},
		{"level HIGH is high", `{"thinkingLevel":"HIGH"}`, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"contents":[{"role":"user","parts":[{"text":"think"}]}],"generationConfig":{"thinkingConfig":` + tc.cfg + `}}`
			f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			up := gemUpstream(t, f)
			if got := up["reasoning_effort"]; got != tc.want {
				t.Errorf("reasoning_effort %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeminiSamplingParams(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}],
	  "generationConfig":{"maxOutputTokens":77,"temperature":0.5,"topP":0.9,"stopSequences":["END"]}}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	up := gemUpstream(t, f)
	if up["max_tokens"] != float64(77) {
		t.Errorf("max_tokens %v", up["max_tokens"])
	}
	if up["temperature"] != float64(0.5) || up["top_p"] != float64(0.9) {
		t.Errorf("temperature %v top_p %v", up["temperature"], up["top_p"])
	}
	stop, _ := up["stop"].([]any)
	if len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop %v", up["stop"])
	}
}

func TestGeminiFinishReasons(t *testing.T) {
	cases := []struct{ upstream, want string }{
		{"stop", "STOP"},
		{"length", "MAX_TOKENS"},
		{"content_filter", "SAFETY"},
	}
	for _, tc := range cases {
		t.Run(tc.upstream, func(t *testing.T) {
			_, code, out := gemRun(t, gemChatReply(tc.upstream), gemGenerate, geminiBody)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			var resp struct {
				Candidates []struct {
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(out), &resp); err != nil || len(resp.Candidates) != 1 {
				t.Fatalf("%v: %s", err, out)
			}
			if got := resp.Candidates[0].FinishReason; got != tc.want {
				t.Errorf("finishReason %s, want %s", got, tc.want)
			}
		})
	}
}

func TestGeminiGenerateContentFunctionCall(t *testing.T) {
	reply := sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)
	f, code, out := gemRun(t, reply, gemGenerate, geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	_ = f
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata map[string]any `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || len(resp.Candidates) != 1 {
		t.Fatalf("%v: %s", err, out)
	}
	cand := resp.Candidates[0]
	if cand.FinishReason != "STOP" || len(cand.Content.Parts) != 1 {
		t.Fatalf("candidate: %s", out)
	}
	fc, _ := cand.Content.Parts[0]["functionCall"].(map[string]any)
	args, _ := fc["args"].(map[string]any)
	if fc["name"] != "read" || fc["id"] != "call_1" || args["path"] != "a.go" {
		t.Errorf("functionCall: %v", fc)
	}
	u := resp.UsageMetadata
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiFunctionResponseMatchedByName(t *testing.T) {
	// neither the call nor its result carries an id
	body := `{"contents":[{"role":"user","parts":[{"text":"read"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}}]}]}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	up := gemUpstream(t, f)
	msgs, _ := up["messages"].([]any)
	var callID, resultID string
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if calls, ok := mm["tool_calls"].([]any); ok && len(calls) > 0 {
			c, _ := calls[0].(map[string]any)
			callID, _ = c["id"].(string)
		}
		if mm["role"] == "tool" {
			resultID, _ = mm["tool_call_id"].(string)
		}
	}
	if callID == "" || callID != resultID {
		t.Errorf("call id %q, result id %q: %s", callID, resultID, f.got)
	}
}

func TestGeminiInlineImagesAndAttachments(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"look"},
	  {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	  {"inlineData":{"mimeType":"application/pdf","data":"JVBER"}}]}]}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	got := string(f.got)
	if !strings.Contains(got, "data:image/png;base64,AAAA") {
		t.Errorf("image not passed: %s", got)
	}
	if !strings.Contains(got, "[attachment application/pdf omitted]") {
		t.Errorf("attachment not replaced by a placeholder: %s", got)
	}
	if strings.Contains(got, "JVBER") {
		t.Errorf("attachment data reached the upstream: %s", got)
	}
}

func TestGeminiInterruptedStreamDropsOpenCall(t *testing.T) {
	reply := sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"error":{"message":"boom"}}`)
	_, code, out := gemRun(t, reply, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	evs := events(out)
	if len(evs) == 0 {
		t.Fatalf("no events: %s", out)
	}
	for _, ev := range evs {
		for _, p := range candParts(t, ev) {
			if _, ok := p["functionCall"]; ok {
				t.Errorf("incomplete call was sent: %s", out)
			}
		}
	}
	last := evs[len(evs)-1]
	cands, _ := last["candidates"].([]any)
	if fin, _ := cands[0].(map[string]any)["finishReason"]; fin != "OTHER" {
		t.Errorf("finishReason %v: %s", fin, out)
	}
}

// gemTruncated is a Chat upstream whose function call has arguments but no
// stop: the stream ends after the call began (or breaks off with an error).
const gemTruncatedCall = `data: {"id":"c1","model":"m1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`

// gemAssertNoCall fails if a Gemini reply (a whole reply or a stream's
// chunks) carries a functionCall, and checks the finish reason when one is
// given.
func gemAssertNoCall(t *testing.T, body string, stream bool, finish string) {
	t.Helper()
	var chunks []map[string]any
	if stream {
		chunks = events(body)
	} else {
		var one map[string]any
		if err := json.Unmarshal([]byte(body), &one); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		chunks = []map[string]any{one}
	}
	if len(chunks) == 0 {
		t.Fatalf("no chunks: %s", body)
	}
	for _, c := range chunks {
		for _, p := range candParts(t, c) {
			if _, ok := p["functionCall"]; ok {
				t.Errorf("incomplete call was sent: %s", body)
			}
		}
	}
	cands, _ := chunks[len(chunks)-1]["candidates"].([]any)
	if fin, _ := cands[0].(map[string]any)["finishReason"]; fin != finish {
		t.Errorf("finishReason %v, want %s: %s", fin, finish, body)
	}
}

func TestGeminiTruncatedCallNotSent(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"upstream error mid-call", sse(gemTruncatedCall, `data: {"error":{"message":"boom"}}`)},
		{"no stop before the end", sse(gemTruncatedCall, `data: [DONE]`)},
	}
	for _, tc := range cases {
		t.Run("stream "+tc.name, func(t *testing.T) {
			_, code, out := gemRun(t, tc.reply, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			gemAssertNoCall(t, out, true, "OTHER")
		})
		t.Run("whole "+tc.name, func(t *testing.T) {
			_, code, out := gemRun(t, tc.reply, gemGenerate, geminiBody)
			if code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			gemAssertNoCall(t, out, false, "OTHER")
		})
	}
}

// gemAnthropicThinking is an Anthropic upstream that thinks, signs the
// thinking in two deltas, then answers "ok".
var gemAnthropicThinking = sse(
	`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":10}}}`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"123"}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
	`data: {"type":"content_block_stop","index":1}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	`data: {"type":"message_stop"}`)

// gemRunAnthropic is gemRun for an Anthropic upstream.
func gemRunAnthropic(t *testing.T, reply, path, body string) (int, string) {
	t.Helper()
	setup(t, provider.Anthropic, &fake{t: t, reply: reply})
	return post(t, path, body)
}

func TestGeminiThoughtSignatureStream(t *testing.T) {
	code, out := gemRunAnthropic(t, gemAnthropicThinking,
		"/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBodyThoughts)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	var thought, sigged string
	for _, ev := range events(out) {
		for _, p := range candParts(t, ev) {
			if p["thought"] == true {
				if s, _ := p["text"].(string); s != "" {
					thought += s
				}
				if s, _ := p["thoughtSignature"].(string); s != "" {
					sigged = s
				}
			}
		}
	}
	if thought != "hmm" || sigged != "sig123" {
		t.Errorf("thought %q signature %q: %s", thought, sigged, out)
	}
}

func TestGeminiThoughtSignatureWhole(t *testing.T) {
	code, out := gemRunAnthropic(t, gemAnthropicThinking, gemGenerate, geminiBodyThoughts)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	var full struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &full); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	found := false
	for _, p := range full.Candidates[0].Content.Parts {
		if p["thought"] == true && p["text"] == "hmm" && p["thoughtSignature"] == "sig123" {
			found = true
		}
	}
	if !found {
		t.Errorf("thinking with signature missing: %s", out)
	}
}

func TestGeminiThoughtsNeedIncludeThoughts(t *testing.T) {
	reply := sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
	for _, path := range []string{"/v1beta/models/fake/m1:streamGenerateContent?alt=sse", gemGenerate} {
		_, code, out := gemRun(t, reply, path, geminiBody)
		if code != 200 {
			t.Fatalf("status %d: %s", code, out)
		}
		if strings.Contains(out, `"thought"`) || strings.Contains(out, "hmm") {
			t.Errorf("%s returned reasoning without includeThoughts: %s", path, out)
		}
		if !strings.Contains(out, `"ok"`) {
			t.Errorf("%s lost the answer: %s", path, out)
		}
	}
}

func TestGeminiAllowedNamesNoneDeclared(t *testing.T) {
	const decls = `[{"functionDeclarations":[{"name":"a","parameters":{"type":"OBJECT"}},` +
		`{"name":"b","parameters":{"type":"OBJECT"}}]}]`
	for _, cfg := range []string{
		`{"mode":"ANY","allowedFunctionNames":["zz"]}`,
		`{"mode":"ANY","allowedFunctionNames":["zz","yy"]}`,
	} {
		body := `{"contents":[{"role":"user","parts":[{"text":"go"}]}],"tools":` + decls +
			`,"toolConfig":{"functionCallingConfig":` + cfg + `}}`
		f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
		if code != 400 || !strings.Contains(out, `"status":"INVALID_ARGUMENT"`) {
			t.Errorf("%s: %d %s", cfg, code, out)
		}
		if len(f.got) != 0 {
			t.Errorf("%s reached the upstream: %s", cfg, f.got)
		}
	}
}

func TestGeminiFunctionResponseWithoutCall(t *testing.T) {
	// no call by that name, and no id to match: refused, the name is not sent as an id
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}}]}]}`
	f, code, out := gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 400 || !strings.Contains(out, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("%d %s", code, out)
	}
	if len(f.got) != 0 {
		t.Errorf("reached the upstream: %s", f.got)
	}
	// with the client's own id, the result is passed on under that id
	body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]},
	  {"role":"user","parts":[{"functionResponse":{"id":"zz","name":"read","response":{"output":"ok"}}}]}]}`
	f, code, out = gemRun(t, gemChatReply("stop"), gemGenerate, body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	msgs, _ := gemUpstream(t, f)["messages"].([]any)
	if last, _ := msgs[len(msgs)-1].(map[string]any); last["tool_call_id"] != "zz" {
		t.Errorf("result id: %v", last)
	}
}
