package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// gemfixStream runs events through a Gemini encoder and returns the decoded
// SSE events.
func gemfixStream(t *testing.T, evs ...Event) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	enc := newGeminiEncoder(newSSEWriter(rec), "fake/m1")
	for _, ev := range evs {
		enc.event(ev)
	}
	enc.finish()
	return gemTestEvents(t, rec.Body.String())
}

// gemfixCalls collects the functionCall objects across events.
func gemfixCalls(evs []map[string]any) []map[string]any {
	var calls []map[string]any
	for _, ev := range evs {
		for _, p := range gemTestParts(ev) {
			if fc, ok := p["functionCall"].(map[string]any); ok {
				calls = append(calls, fc)
			}
		}
	}
	return calls
}

// A call whose arguments stop mid-object is never sent, and the stream still
// ends with a finish event.
func TestGemfixMidStreamCallNotSent(t *testing.T) {
	evs := gemfixStream(t,
		Event{Kind: KStart, MsgID: "up-2"},
		Event{Kind: KToolStart, ID: "c1", Name: "f"},
		Event{Kind: KToolArgs, Text: `{"a":`},
	)
	if calls := gemfixCalls(evs); len(calls) != 0 {
		t.Fatalf("truncated call was sent: %v", calls)
	}
	last := evs[len(evs)-1]
	if c := last["candidates"].([]any)[0].(map[string]any); c["finishReason"] == nil {
		t.Fatalf("no finishReason in final event: %v", last)
	}
}

// A truncated call must not block a complete call that follows it.
func TestGemfixTruncatedCallDoesNotBlockNext(t *testing.T) {
	evs := gemfixStream(t,
		Event{Kind: KToolStart, ID: "c1", Name: "f"},
		Event{Kind: KToolArgs, Text: `{"a":`},
		Event{Kind: KToolStart, ID: "c2", Name: "g"},
		Event{Kind: KToolArgs, Text: `{"b":1}`},
		Event{Kind: KStop, Stop: "tool"},
	)
	calls := gemfixCalls(evs)
	if len(calls) != 1 || calls[0]["id"] != "c2" || calls[0]["name"] != "g" {
		t.Fatalf("calls = %v, want only c2", calls)
	}
}

// Non-object arguments are wrapped as {"input": ...}, in both the stream and
// the whole reply.
func TestGemfixNonObjectArgsWrapped(t *testing.T) {
	evs := gemfixStream(t,
		Event{Kind: KToolStart, ID: "c1", Name: "f"},
		Event{Kind: KToolArgs, Text: `[1]`},
		Event{Kind: KStop, Stop: "tool"},
	)
	calls := gemfixCalls(evs)
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	args, ok := calls[0]["args"].(map[string]any)
	if !ok || len(args) != 1 || args["input"] == nil {
		t.Fatalf("args = %v, want {\"input\": [1]}", calls[0]["args"])
	}

	res := Result{Stop: "tool", Parts: []Part{{Kind: ToolCall, ID: "c2", Name: "g", Args: json.RawMessage(`"x"`)}}}
	var m map[string]any
	if err := json.Unmarshal(renderGemini(res, "m"), &m); err != nil {
		t.Fatal(err)
	}
	fc := gemTestParts(m)[0]["functionCall"].(map[string]any)
	if got := fc["args"].(map[string]any)["input"]; got != "x" {
		t.Fatalf("args = %v, want input \"x\"", fc["args"])
	}
}

// With no content the parts array is present and empty, not null or absent.
func TestGemfixEmptyContentPartsArray(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(renderGemini(Result{Stop: "stop"}, "m"), &m); err != nil {
		t.Fatal(err)
	}
	content := m["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) != 0 {
		t.Fatalf("parts = %v (%T), want empty array", content["parts"], content["parts"])
	}

	evs := gemfixStream(t, Event{Kind: KStart, MsgID: "up-3"})
	last := evs[len(evs)-1]
	content = last["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)
	if parts, ok := content["parts"].([]any); !ok || len(parts) != 0 {
		t.Fatalf("stream parts = %v, want empty array", content["parts"])
	}
}

// An upstream error does not finish with STOP.
func TestGemfixUpstreamErrorFinishIsOther(t *testing.T) {
	evs := gemfixStream(t,
		Event{Kind: KText, Text: "partial"},
		Event{Kind: KError, Text: "boom"},
	)
	last := evs[len(evs)-1]
	c := last["candidates"].([]any)[0].(map[string]any)
	if c["finishReason"] != "OTHER" {
		t.Fatalf("finishReason = %v, want OTHER", c["finishReason"])
	}
	var sawErr bool
	for _, ev := range evs {
		if e, ok := ev["error"].(map[string]any); ok {
			sawErr = true
			if e["status"] != "INTERNAL" || e["message"] != "boom" {
				t.Fatalf("error event = %v", e)
			}
		}
	}
	if !sawErr {
		t.Fatalf("no error event: %v", evs)
	}
}

// A call with invalid arguments is left out of the whole reply too.
func TestGemfixRenderSkipsInvalidArgs(t *testing.T) {
	res := Result{Stop: "tool", Parts: []Part{
		{Kind: ToolCall, ID: "c1", Name: "f", Args: json.RawMessage(`{"a":`)},
		{Kind: ToolCall, ID: "c2", Name: "g", Args: json.RawMessage(`{"b":2}`)},
	}}
	var m map[string]any
	if err := json.Unmarshal(renderGemini(res, "m"), &m); err != nil {
		t.Fatal(err)
	}
	parts := gemTestParts(m)
	if len(parts) != 1 || parts[0]["functionCall"].(map[string]any)["id"] != "c2" {
		t.Fatalf("parts = %v, want only c2", parts)
	}
	if strings.TrimSpace(string(renderGemini(res, "m"))) == "" {
		t.Fatal("empty body")
	}
}
