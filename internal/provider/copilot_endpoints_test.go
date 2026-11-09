package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotEndpointsInitiatorRules(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{"chat user turn", "/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, "user"},
		{"chat tool reply", "/chat/completions", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, "agent"},
		{"anthropic plain user", "/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`, "user"},
		{"anthropic only tool_result", "/v1/messages", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]}`, "agent"},
		{"anthropic tool_result mixed with text", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"text","text":"and also"}]}]}`, "user"},
		{"anthropic last is assistant", "/v1/messages", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`, "agent"},
		{"responses string input", "/responses", `{"input":"hello"}`, "user"},
		{"responses user message", "/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, "user"},
		{"responses function output last", "/responses", `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call","call_id":"c"},{"type":"function_call_output","call_id":"c","output":"x"}]}`, "agent"},
	}
	for _, c := range cases {
		if got := copilotInitiator(c.path, []byte(c.body)); got != c.want {
			t.Errorf("%s: initiator %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCopilotEndpointsVisionDetection(t *testing.T) {
	yes := []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`,
	}
	for _, b := range yes {
		if !copilotHasImage([]byte(b)) {
			t.Errorf("image not seen in %s", b)
		}
	}
	no := `{"messages":[{"role":"user","content":"the string image_url and type image mention nothing"}]}`
	if copilotHasImage([]byte(no)) {
		t.Errorf("text mentioning image_url marked as vision")
	}
}

func TestCopilotEndpointsSupportedMapping(t *testing.T) {
	got := copilotAPIs([]string{"ws:/responses", "/responses", "/chat/completions", "/v1/messages", "/embeddings", "/responses"})
	if want := []string{"responses", "chat", "anthropic"}; !slices.Equal(got, want) {
		t.Fatalf("apis: %v, want %v", got, want)
	}
	if got := copilotAPIs(nil); got != nil {
		t.Fatalf("missing supported_endpoints should be unknown, got %v", got)
	}
}

func TestCopilotEndpointsDeclaresThreeBases(t *testing.T) {
	p := Provider{ID: "copilot", Chat: copilotBase, Responses: copilotBase, Anthropic: copilotBase}
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
}

func TestCopilotEndpointsProviderAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	models := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "bogus"}},
		{ID: "unknown-x"},
	}
	if err := catalog.SaveLive("endpointstest", "", models); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "endpointstest"}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("unknown-x"); got != nil {
		t.Fatalf("unknown-x: %v", got)
	}
	if got := p.APIs("not-listed"); got != nil {
		t.Fatalf("not-listed: %v", got)
	}
	if got := (Provider{ID: "endpointstest-none"}).APIs("gpt-x"); got != nil {
		t.Fatalf("no list: %v", got)
	}
}

func TestCopilotEndpointsCatalogJSONCompatible(t *testing.T) {
	old, _ := json.Marshal(catalog.Model{ID: "m"})
	if got := string(old); strings.Contains(got, "APIs") {
		t.Fatalf("empty APIs should be omitted: %s", got)
	}
	b, _ := json.Marshal(catalog.Model{ID: "m", APIs: []string{"chat"}})
	var back catalog.Model
	if err := json.Unmarshal(b, &back); err != nil || !slices.Equal(back.APIs, []string{"chat"}) {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
