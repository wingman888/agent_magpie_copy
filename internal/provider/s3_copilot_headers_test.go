package provider

import "testing"

func TestS3CopilotHeadersCases(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		body      string
		initiator string
		vision    bool
	}{
		{
			name:      "anthropic pure tool_result is agent",
			path:      "/v1/messages",
			body:      `{"model":"claude-x","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`,
			initiator: "agent",
		},
		{
			name:      "anthropic tool_result mixed with text is user",
			path:      "/v1/messages",
			body:      `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"now do more"}]}]}`,
			initiator: "user",
		},
		{
			name:      "anthropic string content is user",
			path:      "/v1/messages",
			body:      `{"model":"claude-x","messages":[{"role":"user","content":"hello"}]}`,
			initiator: "user",
		},
		{
			name:      "anthropic image block sets vision",
			path:      "/v1/messages",
			body:      `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
			initiator: "user",
			vision:    true,
		},
		{
			name:      "anthropic image nested in tool_result sets vision",
			path:      "/v1/messages",
			body:      `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`,
			initiator: "agent",
			vision:    true,
		},
		{
			name:      "responses string input is user",
			path:      "/responses",
			body:      `{"model":"gpt-x","input":"hello"}`,
			initiator: "user",
		},
		{
			name:      "responses function_call_output is agent",
			path:      "/responses",
			body:      `{"model":"gpt-x","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
			initiator: "agent",
		},
		{
			name:      "responses user message with input_image sets vision",
			path:      "/responses",
			body:      `{"model":"gpt-x","input":[{"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`,
			initiator: "user",
			vision:    true,
		},
		{
			name:      "chat last user is user, image_url sets vision",
			path:      "/chat/completions",
			body:      `{"model":"gpt-x","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			initiator: "user",
			vision:    true,
		},
		{
			name:      "chat tool message is agent",
			path:      "/chat/completions",
			body:      `{"model":"gpt-x","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"c1","content":"x"}]}`,
			initiator: "agent",
		},
		{
			name:      "text mentioning image_url is not vision",
			path:      "/chat/completions",
			body:      `{"model":"gpt-x","messages":[{"role":"user","content":"the string \"image_url\" appears here"}]}`,
			initiator: "user",
		},
	}
	for _, c := range cases {
		gotInit, gotVision := copilotInitiator(c.path, []byte(c.body))
		if gotInit != c.initiator || gotVision != c.vision {
			t.Errorf("%s: got initiator=%q vision=%v, want initiator=%q vision=%v", c.name, gotInit, gotVision, c.initiator, c.vision)
		}
	}
}
