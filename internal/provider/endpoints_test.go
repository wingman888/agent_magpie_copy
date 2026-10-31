package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotModelsReadEndpoints(t *testing.T) {
	signIn(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
		 {"id":"gpt-6","name":"GPT-6","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","ws:/responses"]},
		 {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages","/embeddings"]},
		 {"id":"mystery","name":"M","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p, _ := find(All(), "copilot")
	if len(p.Speaks()) != 3 {
		t.Fatalf("speaks %v", p.Speaks())
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	if got := p.APIs("gpt-6"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-6: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x: %v", got)
	}
	if got := p.APIs("mystery"); got != nil {
		t.Errorf("mystery: %v", got)
	}
	if got := p.APIs("absent"); got != nil {
		t.Errorf("absent: %v", got)
	}
	// each endpoint's self-test asks a model that endpoint serves
	if m := p.testModel(p, Responses); m != "gpt-6" {
		t.Errorf("responses test model %q", m)
	}
	if m := p.testModel(p, Anthropic); m != "claude-x" {
		t.Errorf("anthropic test model %q", m)
	}
	if m := p.testModel(p, Chat); m != "claude-x" {
		t.Errorf("chat test model %q", m)
	}
}

func TestCatalogKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("x", "http://x", []catalog.Model{{ID: "m", APIs: []string{"chat", "responses"}}}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := catalog.Live("x")
	if !ok || !slices.Equal(ms[0].APIs, []string{"chat", "responses"}) {
		t.Fatalf("%+v", ms)
	}
}

func TestCopilotRequestHeaders(t *testing.T) {
	for _, c := range []struct {
		name, body string
		user, img  bool
	}{
		{"anthropic tool_result only", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"tool_use","id":"1"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`, false, false},
		{"anthropic mixed", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"now this"}]}]}`, true, false},
		{"anthropic text", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64"}}]}]}`, true, true},
		{"chat tool", `{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`, true, true},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses user", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses output", `{"input":[{"role":"user","content":"a"},{"type":"function_call_output","call_id":"1","output":"x"}]}`, false, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"u"}]}]}`, true, true},
	} {
		if got := userTurn([]byte(c.body)); got != c.user {
			t.Errorf("%s: user=%v", c.name, got)
		}
		if got := hasImage([]byte(c.body)); got != c.img {
			t.Errorf("%s: image=%v", c.name, got)
		}
	}
}
