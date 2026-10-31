package provider

import (
	"reflect"
	"testing"
)

func TestCopilotInitiatorAndVision(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		user, img  bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"anthropic tool_result only", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"x"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"x"}]}]}`, false, false},
		{"anthropic tool_result + text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"x"},{"type":"text","text":"more"}]}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"a"}}]}]}`, true, true},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses output", `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call_output","call_id":"1","output":"x"}]}`, false, false},
		{"responses user items", `{"input":[{"role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"data:x"}]}]}`, true, true},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, true, true},
	} {
		if got := copilotUserTurn([]byte(tc.body)); got != tc.user {
			t.Errorf("%s: user = %v", tc.name, got)
		}
		if got := copilotHasImage([]byte(tc.body)); got != tc.img {
			t.Errorf("%s: image = %v", tc.name, got)
		}
	}
}

func TestCopilotAPIs(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "ws:/responses", "/responses", "/v1/messages", "/embeddings"})
	if !reflect.DeepEqual(got, []string{"chat", "responses", "anthropic"}) {
		t.Errorf("%v", got)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings"}) != nil {
		t.Error("none known should be nil")
	}
}
