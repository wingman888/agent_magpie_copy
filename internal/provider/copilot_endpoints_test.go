package provider

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotAPIsFromSupportedEndpoints(t *testing.T) {
	got := copilotAPIs([]string{"/responses", "ws:/responses", "/v1/messages", "/embeddings", "/chat/completions", "/responses"})
	if strings.Join(got, ",") != "responses,anthropic,chat" {
		t.Fatalf("apis: %v", got)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings"}) != nil {
		t.Fatal("a missing or unrecognised list must be unknown (nil)")
	}
}

func TestCopilotProviderDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "octocat"}, "Pro")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
	if p.Responses != copilotBase || p.Anthropic != copilotBase || p.Chat != copilotBase {
		t.Fatalf("bases: %+v", p)
	}
}

func TestProviderAPIsReadsLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("apisdemo", "https://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "apisdemo"}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Fatalf("a model without APIs is unknown: %v", got)
	}
	if got := p.APIs("missing"); got != nil {
		t.Fatalf("an unlisted model is unknown: %v", got)
	}
	if got := p.APIs("vendor/gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("vendor-prefixed id: %v", got)
	}
	if got := (Provider{ID: "nolist"}).APIs("gpt-x"); got != nil {
		t.Fatalf("no list: %v", got)
	}
}

func TestProviderAPIsOldCacheStillReadable(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("oldcache", "", []catalog.Model{{ID: "m1", Name: "M1"}}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := catalog.Live("oldcache")
	if !ok || len(ms) != 1 || ms[0].APIs != nil {
		t.Fatalf("old cache: %+v %v", ms, ok)
	}
}

func TestCopilotUserTurn(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"chat user last":            {`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, true},
		"chat tool last":            {`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false},
		"anthropic text user":       {`{"messages":[{"role":"user","content":"hi"}]}`, true},
		"anthropic all tool_result": {`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x"}]}]}`, false},
		"anthropic mixed text":      {`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x"},{"type":"text","text":"now"}]}]}`, true},
		"anthropic assistant last":  {`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"a"}]}]}`, false},
		"responses string":          {`{"input":"hello"}`, true},
		"responses user message":    {`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true},
		"responses function output": {`{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"x"}]}`, false},
		"responses assistant last":  {`{"input":[{"type":"message","role":"assistant","content":"x"}]}`, false},
	}
	for name, c := range cases {
		if got := copilotUserTurn([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func TestCopilotHasImage(t *testing.T) {
	yes := []string{
		`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"image","source":{}}]}]}]}`,
	}
	for _, b := range yes {
		if !copilotHasImage([]byte(b)) {
			t.Errorf("no vision: %s", b)
		}
	}
	no := `{"messages":[{"role":"user","content":"just words"}],"tools":[{"name":"t","input_schema":{"type":"object"}}]}`
	if copilotHasImage([]byte(no)) {
		t.Error("text-only request flagged as vision")
	}
}

func TestCopilotSignInitiatorAndVision(t *testing.T) {
	// a live session for the (empty) GitHub token, so signing asks no one
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{"": {Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
	copilotMu.Unlock()
	p := copilotProvider(copilotApp{User: "octocat"}, "")
	cases := []struct {
		name, path, body, initiator, vision string
	}{
		{"anthropic tool_result only", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x"}]}]}`, "agent", ""},
		{"anthropic tool_result with text", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x"},{"type":"text","text":"y"}]}]}`, "user", ""},
		{"responses string", "/responses", `{"input":"hi"}`, "user", ""},
		{"responses image", "/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`, "user", "true"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("POST", p.Responses+c.path, nil)
		if err := p.Sign(context.Background(), req, Chat, []byte(c.body)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := req.Header.Get("X-Initiator"); got != c.initiator {
			t.Errorf("%s: X-Initiator %q, want %q", c.name, got, c.initiator)
		}
		if got := req.Header.Get("Copilot-Vision-Request"); got != c.vision {
			t.Errorf("%s: Copilot-Vision-Request %q, want %q", c.name, got, c.vision)
		}
	}
}
