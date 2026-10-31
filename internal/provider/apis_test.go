package provider

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotAPIsMapping(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "ws:/responses", "/v1/embeddings", "/responses", "/v1/messages", "/responses"})
	if want := []string{"chat", "responses", "anthropic"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings"}) != nil {
		t.Fatal("unknown must be nil")
	}
}

func TestProviderAPIsAndTestModel(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "x", Name: "X", Chat: "http://c", Responses: "http://r", Anthropic: "http://a"}
	if p.APIs("gpt-x") != nil {
		t.Fatal("no list: unknown")
	}
	if err := catalog.SaveLive("x", "http://c", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "bogus"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if p.APIs("plain") != nil || p.APIs("nope") != nil {
		t.Fatal("unknown must be nil")
	}
	if m := p.testModel(p, Chat); m != "claude-x" {
		t.Fatalf("chat probe: %q", m)
	}
	if m := p.testModel(p, Responses); m != "gpt-x" {
		t.Fatalf("responses probe: %q", m)
	}
	if m := p.testModel(p, Anthropic); m != "claude-x" {
		t.Fatalf("anthropic probe: %q", m)
	}
}

func TestCopilotHeaders(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		user   bool
		vision bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`, true, true},
		{"anthropic tool_result only", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"x"}]}]}`, false, false},
		{"anthropic tool_result + text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"x"},{"type":"text","text":"more"}]}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`, true, true},
		{"anthropic text mentioning image", `{"messages":[{"role":"user","content":"\"type\":\"image\""}]}`, true, false},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses output", `{"input":[{"type":"message","role":"user","content":"a"},{"type":"function_call_output","call_id":"1","output":"x"}]}`, false, false},
		{"responses user msg", `{"input":[{"role":"user","content":[{"type":"input_text","text":"a"}]}]}`, true, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"u"}]}]}`, true, true},
	}
	for _, c := range cases {
		if got := copilotUserTurn([]byte(c.body)); got != c.user {
			t.Errorf("%s: user=%v want %v", c.name, got, c.user)
		}
		if got := copilotHasImage([]byte(c.body)); got != c.vision {
			t.Errorf("%s: vision=%v want %v", c.name, got, c.vision)
		}
	}
}

func TestCopilotProviderEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{Token: "t"}, "")
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" {
		t.Fatalf("copilot must declare all three: %+v", p.Speaks())
	}
	_ = http.MethodGet
}
