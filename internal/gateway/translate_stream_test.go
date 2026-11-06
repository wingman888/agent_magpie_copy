package gateway

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatToResponses runs Chat Completions stream chunks through the decoder and
// the Responses encoder and returns every event written.
func chatToResponses(t *testing.T, chunks ...string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	enc := &responsesEncoder{w: newSSEWriter(rec), model: "m"}
	var d chatDecoder
	for _, c := range chunks {
		if err := d.decode(c, enc.event); err != nil {
			t.Fatal(err)
		}
	}
	enc.finish()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var m map[string]any
			if json.Unmarshal([]byte(data), &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// #18: with parallel tool calls each function_call item kept the next call's
// id, and the last two shared one — upstreams then refused the history.
func TestParallelToolCallIDs(t *testing.T) {
	var chunks []string
	for i, id := range []string{"call_00", "call_01", "call_02"} {
		chunks = append(chunks, `{"id":"x","choices":[{"delta":{"tool_calls":[{"index":`+string(rune('0'+i))+`,"id":"`+id+`","type":"function","function":{"name":"run","arguments":""}}]}}]}`,
			`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":`+string(rune('0'+i))+`,"function":{"arguments":"{\"cmd\":\"`+id+`\"}"}}]}}]}`)
	}
	chunks = append(chunks, `{"id":"x","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	var got []string
	for _, ev := range chatToResponses(t, chunks...) {
		if ev["type"] != "response.output_item.done" {
			continue
		}
		item := ev["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		var args struct{ Cmd string }
		json.Unmarshal([]byte(item["arguments"].(string)), &args)
		if item["call_id"] != args.Cmd {
			t.Errorf("call %s went out with id %v", args.Cmd, item["call_id"])
		}
		got = append(got, item["call_id"].(string))
	}
	if strings.Join(got, ",") != "call_00,call_01,call_02" {
		t.Fatalf("call ids = %v", got)
	}
}

// Some relays put the same thought under reasoning_content and reasoning;
// it must come out once, not "TheThe user user".
func TestReasoningSentUnderBothNames(t *testing.T) {
	var think strings.Builder
	for _, ev := range chatToResponses(t,
		`{"id":"x","choices":[{"delta":{"reasoning_content":"The ","reasoning":"The "}}]}`,
		`{"id":"x","choices":[{"delta":{"reasoning_content":"user","reasoning":"user"}}]}`,
		`{"id":"x","choices":[{"delta":{"reasoning":" wants"}}]}`,
		`{"id":"x","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`) {
		if ev["type"] == "response.reasoning_summary_text.delta" {
			think.WriteString(ev["delta"].(string))
		}
	}
	if think.String() != "The user wants" {
		t.Fatalf("thinking = %q", think.String())
	}
}
