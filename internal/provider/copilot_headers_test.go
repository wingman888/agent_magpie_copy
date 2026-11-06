package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// copilotUpstream stands in for Copilot's API: it serves a model list that
// declares supported_endpoints and records what each request carried.
type copilotUpstream struct {
	mu   sync.Mutex
	seen []copilotSeen
}

type copilotSeen struct {
	path   string
	header http.Header
	body   string
}

func (u *copilotUpstream) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
			  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, copilotSeen{path: r.URL.Path, header: r.Header.Clone(), body: string(b)})
		u.mu.Unlock()
		w.Write([]byte(`{}`))
	}
}

// copilotHarness signs in, points Copilot's token and API at a test server,
// and returns the Copilot provider with its model list fetched.
func copilotHarness(t *testing.T) (Provider, *copilotUpstream) {
	t.Helper()
	signIn(t)
	u := &copilotUpstream{}
	api := httptest.NewServer(u.handler(t))
	t.Cleanup(api.Close)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(tokens.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	copilotSessions = map[string]copilotSession{}

	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("copilot provider not found")
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	return p, u
}

// send signs a request the way the gateway does and delivers it to the
// upstream, returning the headers the upstream saw.
func copilotSend(t *testing.T, p Provider, u *copilotUpstream, proto Protocol, path, body string) http.Header {
	t.Helper()
	req, _ := http.NewRequest("POST", p.Base(proto)+path, strings.NewReader(body))
	if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.seen) == 0 {
		t.Fatal("upstream saw no request")
	}
	return u.seen[len(u.seen)-1].header
}

func TestCopilotInitiatorAndVision(t *testing.T) {
	p, u := copilotHarness(t)
	cases := []struct {
		name   string
		proto  Protocol
		path   string
		body   string
		user   bool
		vision bool
	}{
		{"chat user text", Chat, "/chat/completions",
			`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool reply", Chat, "/chat/completions",
			`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", Chat, "/chat/completions",
			`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, true, true},
		{"chat word image_url in text is not a picture", Chat, "/chat/completions",
			`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"the image_url field"}]}`, true, false},
		{"anthropic only tool_result", Anthropic, "/v1/messages",
			`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result mixed with text", Anthropic, "/v1/messages",
			`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"now do more"}]}]}`, true, false},
		{"anthropic string content", Anthropic, "/v1/messages",
			`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, true, false},
		{"anthropic image block", Anthropic, "/v1/messages",
			`{"model":"claude-sonnet-5","max_tokens":8,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"text","text":"what is this"}]}]}`, true, true},
		{"responses string input", Responses, "/responses",
			`{"model":"gpt-6-sol","input":"hi"}`, true, false},
		{"responses function output last", Responses, "/responses",
			`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, false, false},
		{"responses user message with image", Responses, "/responses",
			`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := copilotSend(t, p, u, c.proto, c.path, c.body)
			if got := h.Get("X-Initiator") == "user"; got != c.user {
				t.Errorf("X-Initiator user=%v, want %v (headers %v)", got, c.user, h)
			}
			if got := h.Get("Copilot-Vision-Request") == "true"; got != c.vision {
				t.Errorf("Copilot-Vision-Request=%v, want %v", got, c.vision)
			}
		})
	}
}

func TestCopilotSelfTestPicksModelForEndpoint(t *testing.T) {
	p, _ := copilotHarness(t)
	if got := p.APIs("gpt-6-sol"); len(got) != 1 || got[0] != Responses {
		t.Fatalf("gpt-6-sol apis: %v", got)
	}
	res := p.Test(context.Background())
	models := map[Protocol]string{}
	for _, r := range res {
		models[r.Protocol] = r.Model
	}
	if models[Responses] != "gpt-6-sol" {
		t.Errorf("responses probe model = %q, want gpt-6-sol (%+v)", models[Responses], res)
	}
	if models[Chat] != "claude-sonnet-5" {
		t.Errorf("chat probe model = %q, want claude-sonnet-5 (%+v)", models[Chat], res)
	}
	if models[Anthropic] != "claude-sonnet-5" {
		t.Errorf("anthropic probe model = %q, want claude-sonnet-5 (%+v)", models[Anthropic], res)
	}
}
