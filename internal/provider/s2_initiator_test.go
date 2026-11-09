package provider

import "testing"

// Copilot's X-Initiator and Copilot-Vision-Request follow the last turn in
// whichever of the three wire shapes the request is in.
func TestS2CopilotInitiatorAndVision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		user   bool
		vision bool
	}{
		{"chat user text", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool last", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`, true, true},
		{"anthropic only tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result mixed with text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"next"}]}]}`, true, false},
		{"anthropic string content", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"anthropic image block", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`, true, true},
		{"anthropic image in tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}]}`, false, true},
		{"responses string input", `{"model":"m","input":"hi"}`, true, false},
		{"responses user message", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses function output last", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, false, false},
		{"responses image", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"}]}]}`, true, true},
		{"empty body", `{}`, false, false},
	} {
		if got := userTurn([]byte(tc.body)); got != tc.user {
			t.Errorf("%s: userTurn = %v, want %v", tc.name, got, tc.user)
		}
		if got := hasImage([]byte(tc.body)); got != tc.vision {
			t.Errorf("%s: hasImage = %v, want %v", tc.name, got, tc.vision)
		}
	}
}
