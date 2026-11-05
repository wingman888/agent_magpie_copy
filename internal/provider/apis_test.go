package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// isolatedHome points the caches at a temp dir so no test touches the real one.
func isolatedHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestProviderAPIsFromLastList(t *testing.T) {
	isolatedHome(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1"}
	if got := p.APIs("gpt-x"); got != nil {
		t.Fatalf("no list yet: %v", got)
	}
	if err := catalog.SaveLive("relay", p.Chat, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "bogus"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("declared: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("declared, unknown ignored: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Fatalf("undeclared is nil: %v", got)
	}
	if got := p.APIs("never-listed"); got != nil {
		t.Fatalf("unknown is nil: %v", got)
	}
	if got := p.APIs("GPT-X"); got != nil {
		t.Fatalf("ids match exactly: %v", got)
	}
	// the list is per provider
	if other := (Provider{ID: "other"}); other.APIs("gpt-x") != nil {
		t.Fatal("another provider's list leaked")
	}
}

func TestCopilotAPIsFromSupportedEndpoints(t *testing.T) {
	isolatedHome(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/responses","/websocket"]},
		  {"id":"claude-sonnet-5","name":"Claude","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/v1/messages","/chat/completions"]},
		  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/websocket","/embeddings"]}]}`))
	}))
	defer api.Close()
	const tok = "gho_apis_test"
	copilotMu.Lock()
	copilotSessions[tok] = copilotSession{Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	s := copilotSessions[tok]
	s.Endpoints.API = api.URL
	copilotSessions[tok] = s
	copilotMu.Unlock()

	ms, err := copilotModels(context.Background(), copilotApp{User: "u", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("copilot", copilotBase, ms); err != nil {
		t.Fatal(err)
	}
	p := copilotProvider(copilotApp{User: "u", Token: tok}, "Pro")
	cases := map[string][]Protocol{
		"gpt-6-sol":       {Responses},
		"claude-sonnet-5": {Anthropic, Chat},
		"gpt-4.1":         nil, // no list: unknown
		"gpt-5.5":         nil, // only endpoints we don't route
	}
	for id, want := range cases {
		if got := p.APIs(id); !slices.Equal(got, want) {
			t.Errorf("%s: got %v want %v", id, got, want)
		}
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"/websocket"}, nil},
		{[]string{"/responses", "/chat/completions", "/v1/messages", "/responses"}, []string{"responses", "chat", "anthropic"}},
	}
	for _, c := range cases {
		if got := copilotAPIs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestCopilotProviderDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "u", Token: "x"}, "Pro")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
	if p.Base(Responses) != copilotBase || p.Base(Anthropic) != copilotBase {
		t.Fatalf("bases: %q %q", p.Base(Responses), p.Base(Anthropic))
	}
}

// signedCopilot is a Copilot provider whose session is already cached, so
// signing needs no network; requests land on a server that is never asked.
func signedCopilot(t *testing.T) Provider {
	t.Helper()
	isolatedHome(t)
	const tok = "gho_sign_test"
	copilotMu.Lock()
	s := copilotSession{Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	s.Endpoints.API = "https://copilot.test"
	copilotSessions[tok] = s
	copilotMu.Unlock()
	return copilotProvider(copilotApp{User: "u", Token: tok}, "Pro")
}

func TestCopilotInitiatorAndVisionHeaders(t *testing.T) {
	p := signedCopilot(t)
	cases := []struct {
		name      string
		proto     Protocol
		body      string
		initiator string
		vision    bool
	}{
		{"chat user text", Chat, `{"messages":[{"role":"user","content":"hi"}]}`, "user", false},
		{"chat tool reply", Chat, `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, "agent", false},
		{"chat image", Chat, `{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, "user", true},
		{"anthropic string user", Anthropic, `{"messages":[{"role":"user","content":"hi"}]}`, "user", false},
		{"anthropic pure tool_result", Anthropic, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`, "agent", false},
		{"anthropic tool_result with text", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"},{"type":"text","text":"now do more"}]}]}`, "user", false},
		{"anthropic image block", Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`, "user", true},
		{"anthropic image inside tool_result", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{}}]}]}]}`, "agent", true},
		{"responses string input", Responses, `{"model":"m","input":"hi"}`, "user", false},
		{"responses user message", Responses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, "user", false},
		{"responses function output", Responses, `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`, "agent", false},
		{"responses image", Responses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"},{"type":"input_image","image_url":"data:x"}]}]}`, "user", true},
		{"text mentioning image type is not an image", Chat, `{"messages":[{"role":"user","content":"\"type\":\"image_url\""}]}`, "user", false},
		{"no body", Chat, ``, "agent", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://copilot.test/x", nil)
			if err := p.Sign(context.Background(), req, c.proto, []byte(c.body)); err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("X-Initiator"); got != c.initiator {
				t.Errorf("X-Initiator %q, want %q", got, c.initiator)
			}
			if got := req.Header.Get("Copilot-Vision-Request") == "true"; got != c.vision {
				t.Errorf("Copilot-Vision-Request present=%v, want %v", got, c.vision)
			}
		})
	}
}

func TestTranslationOrder(t *testing.T) {
	openai := Provider{ID: "openai", Name: "OpenAI", Chat: "https://api.openai.com/v1"}
	copilot := copilotProvider(copilotApp{User: "u", Token: "x"}, "Pro")
	relay := Provider{ID: "relay", Name: "Relay", Chat: "https://openrouter.example/v1"}
	cases := []struct {
		name  string
		p     Provider
		model string
		want  []Protocol
	}{
		{"openai gpt", openai, "gpt-6-sol", []Protocol{Responses, Chat}},
		{"openai o-series", openai, "o4-mini", []Protocol{Responses, Chat}},
		{"openai codex", openai, "codex-mini", []Protocol{Responses, Chat}},
		{"openai prefixed", openai, "openai/gpt-6-sol", []Protocol{Responses, Chat}},
		{"openai non-openai model", openai, "claude-sonnet-5", []Protocol{Chat, Responses}},
		{"openai o without digit", openai, "omni-moderation", []Protocol{Chat, Responses}},
		{"copilot gpt", copilot, "gpt-6-sol", []Protocol{Responses, Chat}},
		{"copilot o-series", copilot, "o4-mini", []Protocol{Responses, Chat}},
		{"copilot claude", copilot, "claude-sonnet-5", []Protocol{Chat, Responses}},
		{"other vendor gpt-oss", relay, "openai/gpt-oss-120b", []Protocol{Chat, Responses}},
		{"other vendor gpt", relay, "gpt-6-sol", []Protocol{Chat, Responses}},
	}
	for _, c := range cases {
		if got := TranslationOrder(c.p, c.model); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// The self-test asks each endpoint for a model the vendor's list says it
// serves there, and for nothing when the list has none.
func TestTestPicksModelsByDeclaredAPIs(t *testing.T) {
	isolatedHome(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Anthropic: "https://relay.example"}
	if err := catalog.SaveLive("relay", p.Chat, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.testModel(p, Responses); got != "gpt-x" {
		t.Errorf("responses: %q", got)
	}
	if got := p.testModel(p, Chat); got != "claude-x" {
		t.Errorf("chat: %q", got)
	}
	if got := p.testModel(p, Anthropic); got != "" {
		t.Errorf("anthropic: %q, want none", got)
	}
}

func TestTestWithoutListKeepsOldChoice(t *testing.T) {
	isolatedHome(t)
	p := Provider{ID: "bare", Name: "Bare", Chat: "https://bare.example/v1", Anthropic: "https://bare.example", Models: []string{"claude-opus-5", "gpt-x"}}
	if got := p.testModel(p, Anthropic); got != "claude-opus-5" {
		t.Errorf("anthropic without a list: %q", got)
	}
	if got := p.testModel(p, Responses); got != "claude-opus-5" {
		t.Errorf("responses without a list: %q", got)
	}
}
