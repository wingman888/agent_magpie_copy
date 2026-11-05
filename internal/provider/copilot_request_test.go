package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilotTestAccount signs requests with a cached session, so no network.
func copilotTestAccount(t *testing.T) Provider {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := copilotSessions
	copilotSessions = map[string]copilotSession{
		"gho_hdr": {Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}
	t.Cleanup(func() { copilotSessions = old })
	return copilotProvider(copilotApp{User: "octocat", Token: "gho_hdr"}, "")
}

func TestCopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotTestAccount(t)
	if p.Chat != copilotBase || p.Responses != copilotBase || p.Anthropic != copilotBase {
		t.Fatalf("endpoints: chat=%q responses=%q anthropic=%q", p.Chat, p.Responses, p.Anthropic)
	}
	if got := p.Speaks(); len(got) != 3 || got[0] != Chat || got[1] != Responses || got[2] != Anthropic {
		t.Fatalf("speaks: %v", got)
	}
}

func TestCopilotInitiatorAndVisionHeaders(t *testing.T) {
	p := copilotTestAccount(t)
	cases := []struct {
		name      string
		proto     Protocol
		path      string
		body      string
		initiator string
		vision    bool
	}{
		{"chat user text", Chat, "/chat/completions",
			`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, "user", false},
		{"chat tool last", Chat, "/chat/completions",
			`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, "agent", false},
		{"chat image", Chat, "/chat/completions",
			`{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, "user", true},
		{"anthropic only tool_result", Anthropic, "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`, "agent", false},
		{"anthropic tool_result mixed with text", Anthropic, "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"and"}]}]}`, "user", false},
		{"anthropic string content", Anthropic, "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":"hello"}]}`, "user", false},
		{"anthropic image block", Anthropic, "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}},{"type":"text","text":"what"}]}]}`, "user", true},
		{"anthropic image inside tool result", Anthropic, "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{}}]}]}]}`, "agent", true},
		{"responses string input", Responses, "/responses",
			`{"model":"gpt-5.5","input":"hi"}`, "user", false},
		{"responses function output last", Responses, "/responses",
			`{"model":"gpt-5.5","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, "agent", false},
		{"responses user message array", Responses, "/responses",
			`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:x"}]}]}`, "user", true},
		{"schema property named image is not vision", Chat, "/chat/completions",
			`{"model":"gpt-5.5","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"image":{"type":"image"}}}}}],"messages":[{"role":"user","content":"hi"}]}`, "user", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, p.Base(c.proto)+c.path, nil)
			if err := p.Sign(context.Background(), req, c.proto, []byte(c.body)); err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("X-Initiator"); got != c.initiator {
				t.Errorf("X-Initiator = %q, want %q", got, c.initiator)
			}
			if got := req.Header.Get("Copilot-Vision-Request"); (got == "true") != c.vision {
				t.Errorf("Copilot-Vision-Request = %q, want vision=%v", got, c.vision)
			}
		})
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "websocket", "/responses", "/v1/messages", "/embeddings", "/responses"})
	if strings.Join(got, ",") != "chat,responses,anthropic" {
		t.Fatalf("mapped: %v", got)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings", "ws:/x"}) != nil {
		t.Fatal("unknown or unrecognised endpoints must map to nil")
	}
}

func TestCopilotModelsReadSupportedEndpoints(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
		  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","websocket"]},
		  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]},
		  {"id":"legacy","name":"Legacy","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	p := copilotTestAccount(t)
	s := copilotSession{Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	s.Endpoints.API = api.URL
	copilotSessions["gho_hdr"] = s

	ms, err := copilotModels(context.Background(), copilotApp{Token: "gho_hdr"})
	if err != nil {
		t.Fatal(err)
	}
	apis := map[string]string{}
	for _, m := range ms {
		apis[m.ID] = strings.Join(m.APIs, ",")
	}
	if apis["gpt-6-sol"] != "responses" || apis["claude-x"] != "chat,anthropic" || apis["legacy"] != "" {
		t.Fatalf("apis: %v", apis)
	}

	if err := catalog.SaveLive("copilot", copilotBase, ms); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-6-sol"); len(got) != 1 || got[0] != Responses {
		t.Fatalf("APIs(gpt-6-sol) = %v", got)
	}
	if got := p.APIs("claude-x"); len(got) != 2 || got[0] != Chat || got[1] != Anthropic {
		t.Fatalf("APIs(claude-x) = %v", got)
	}
	if got := p.APIs("legacy"); got != nil {
		t.Fatalf("APIs(legacy) = %v, want nil", got)
	}
	if got := p.APIs("unknown"); got != nil {
		t.Fatalf("APIs(unknown) = %v, want nil", got)
	}
}

func TestProviderAPIsWithoutList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := (Provider{ID: "nolist"}).APIs("gpt-5.5"); got != nil {
		t.Fatalf("no list: %v", got)
	}
}

func TestSelfCheckPicksModelForEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("selfcheck", "http://x", []catalog.Model{
		{ID: "gpt-x", Name: "GPT X", APIs: []string{"responses"}},
		{ID: "claude-x", Name: "Claude X", APIs: []string{"chat"}},
		{ID: "plain", Name: "Plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "selfcheck"}
	if got := p.testModel(p, Responses); got != "gpt-x" {
		t.Errorf("responses probe model = %q, want gpt-x", got)
	}
	if got := p.testModel(p, Anthropic); got != "plain" {
		t.Errorf("anthropic probe model = %q, want plain (claude-x does not declare anthropic)", got)
	}
	if got := p.testModel(p, Chat); got != "claude-x" {
		t.Errorf("chat probe model = %q, want claude-x", got)
	}
}

func TestCopilotAPIsSurviveJSONRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	want := []catalog.Model{{ID: "a", APIs: []string{"chat", "anthropic"}}, {ID: "b"}}
	if err := catalog.SaveLive("roundtrip", "http://x", want); err != nil {
		t.Fatal(err)
	}
	got, _, ok := catalog.Live("roundtrip")
	if !ok || len(got) != 2 || strings.Join(got[0].APIs, ",") != "chat,anthropic" || got[1].APIs != nil {
		t.Fatalf("round trip: %+v", got)
	}
	b, _ := json.Marshal(want[1])
	if strings.Contains(string(b), "apis") {
		t.Fatalf("empty APIs must be omitted: %s", b)
	}
}
