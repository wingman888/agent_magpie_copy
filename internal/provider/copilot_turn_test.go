package provider

import "testing"

// Acceptance 10: X-Initiator and Copilot-Vision-Request follow the wire API
// the request is sent on, not one shape for all of them.
func TestCopilotUserTurnByAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		want bool
	}{
		{"anthropic text user turn", "/v1/messages",
			`{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"anthropic only tool results", "/v1/messages",
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`, false},
		{"anthropic tool result mixed with text", "/v1/messages",
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"},{"type":"text","text":"now do more"}]}]}`, true},
		{"responses string input", "/responses",
			`{"model":"m","input":"hi"}`, true},
		{"responses user message", "/responses",
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true},
		{"responses function output", "/responses",
			`{"model":"m","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`, false},
		{"chat user turn", "/chat/completions",
			`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, true},
		{"chat tool turn", "/chat/completions",
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"c","content":"ok"}]}`, false},
	} {
		if got := copilotUserTurn(tc.path, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: copilotUserTurn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCopilotHasImageByAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		want bool
	}{
		{"chat image_url", "/chat/completions",
			`{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`, true},
		{"chat text only", "/chat/completions",
			`{"messages":[{"role":"user","content":"image_url is a word"}]}`, false},
		{"responses input_image", "/responses",
			`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"}]}]}`, true},
		{"anthropic image block", "/v1/messages",
			`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`, true},
		{"anthropic image in a tool result", "/v1/messages",
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}]}`, true},
		{"anthropic text only", "/v1/messages",
			`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, false},
	} {
		if got := copilotHasImage(tc.path, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: copilotHasImage = %v, want %v", tc.name, got, tc.want)
		}
	}
}
