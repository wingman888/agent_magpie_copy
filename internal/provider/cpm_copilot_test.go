package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cpmModelRows is a Copilot model list covering every filter rule.
const cpmModelRows = `{"data":[
 {"id":"gpt-4o","name":"GPT-4o","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"auto","name":"Auto","capabilities":{"type":"chat"}},
 {"id":"internal-router","capabilities":{"type":"chat"},"internal":true},
 {"id":"hidden-picker","capabilities":{"type":"chat"},"model_picker_enabled":false},
 {"id":"policy-off","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
 {"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
 {"id":"claude-sonnet-4","name":"Claude Sonnet 4","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}}
]}`

// cpmCopilotServer answers as the Copilot token endpoint and as its own API.
// The session is accepted only with the bearer token it issued; the model
// list answers with modelsStatus when it is not 200.
func cpmCopilotServer(t *testing.T, modelsStatus int) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			if r.Header.Get("Authorization") != "Bearer cpm-session" {
				http.Error(w, "no session", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Copilot-Integration-Id") != "vscode-chat" || r.Header.Get("Editor-Version") == "" {
				http.Error(w, "missing editor headers", http.StatusBadRequest)
				return
			}
			if modelsStatus != http.StatusOK {
				http.Error(w, "denied", modelsStatus)
				return
			}
			w.Write([]byte(cpmModelRows))
			return
		}
		if r.Header.Get("Authorization") != "token gho_test" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "cpm-session",
			"endpoints": map[string]string{"api": srv.URL},
		})
	}))
	t.Cleanup(srv.Close)
	oldURL := copilotTokenURL
	copilotTokenURL = srv.URL
	t.Cleanup(func() { copilotTokenURL = oldURL })
	return srv
}

func TestCpmFilterRules(t *testing.T) {
	var doc struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(cpmModelRows), &doc); err != nil {
		t.Fatal(err)
	}
	got := copilotChatModels(doc.Data)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-4o,claude-sonnet-4" {
		t.Fatalf("filtered = %v, want only chat models that are enabled", ids)
	}
	if got[1].Name != "Claude Sonnet 4" {
		t.Errorf("display name = %q", got[1].Name)
	}
}

func TestCpmFilterDedupesAndKeepsPolicyUnset(t *testing.T) {
	var doc struct {
		Data []copilotModel `json:"data"`
	}
	raw := `{"data":[
	 {"id":"m1","capabilities":{"type":"chat"}},
	 {"id":"m1","capabilities":{"type":"chat"}},
	 {"id":"m2"}
	]}`
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	got := copilotChatModels(doc.Data)
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("got %+v, want only m1 once", got)
	}
}

func TestCpmRequestHeadersInitiator(t *testing.T) {
	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	cases := []struct {
		name string
		to   Protocol
		body string
		want string
	}{
		{"user turn", Chat, `{"messages":[{"role":"user","content":"hi"}]}`, "user"},
		{"tool result last", Chat, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`, "agent"},
		{"anthropic tool_result", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]}]}`, "agent"},
		{"responses function_call_output", Responses, `{"input":[{"type":"message","role":"user"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, "agent"},
		{"responses plain string", Responses, `{"input":"hi"}`, "user"},
		{"bad json defaults to user", Chat, `not json`, "user"},
	}
	for _, c := range cases {
		h := RequestHeaders(cp, c.to, []byte(c.body))
		if h["X-Initiator"] != c.want {
			t.Errorf("%s: X-Initiator = %q, want %q", c.name, h["X-Initiator"], c.want)
		}
		if _, ok := h["Copilot-Vision-Request"]; ok {
			t.Errorf("%s: vision flag set without an image", c.name)
		}
	}
}

func TestCpmRequestHeadersVision(t *testing.T) {
	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	chatImg := `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	if h := RequestHeaders(cp, Chat, []byte(chatImg)); h["Copilot-Vision-Request"] != "true" || h["X-Initiator"] != "user" {
		t.Errorf("chat image headers = %v", h)
	}
	respImg := `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`
	if h := RequestHeaders(cp, Responses, []byte(respImg)); h["Copilot-Vision-Request"] != "true" {
		t.Errorf("responses image headers = %v", h)
	}
}

func TestCpmRequestHeadersOnlyForCopilot(t *testing.T) {
	cx := accountProvider(CodexID, Account{ID: CodexID}, nil)
	if h := RequestHeaders(cx, Responses, []byte(`{"input":"hi"}`)); h != nil {
		t.Errorf("codex got copilot headers: %v", h)
	}
	plain := Provider{ID: "openai", Chat: "https://example.test"}
	if h := RequestHeaders(plain, Chat, []byte(`{"messages":[]}`)); h != nil {
		t.Errorf("plain provider got headers: %v", h)
	}
}

func TestCpmCheckStatus(t *testing.T) {
	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := CheckStatus(cp, st)
		if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
			t.Errorf("status %d: got %v, want re-login hint", st, err)
		}
	}
	if err := CheckStatus(cp, http.StatusOK); err != nil {
		t.Errorf("200 should pass, got %v", err)
	}
	if err := CheckStatus(accountProvider(CodexID, Account{ID: CodexID}, nil), http.StatusUnauthorized); err != nil {
		t.Errorf("codex status must be left alone, got %v", err)
	}
}

func TestCpmUpstreamCarriesEditorHeaders(t *testing.T) {
	acctEnv(t)
	acctWriteCopilot(t)
	cpmCopilotServer(t, http.StatusOK)
	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	_, h, err := Upstream(context.Background(), cp, Chat)
	if err != nil {
		t.Fatal(err)
	}
	if h["Authorization"] != "Bearer cpm-session" || h["Copilot-Integration-Id"] != "vscode-chat" || h["Editor-Version"] == "" {
		t.Errorf("upstream headers = %v", h)
	}
}

func TestCpmFetchRefreshesAndFallsBack(t *testing.T) {
	acctEnv(t)
	acctWriteCopilot(t)
	cpmCopilotServer(t, http.StatusOK)

	cp := accountProvider(CopilotID, Account{ID: CopilotID, User: "octocat"}, nil)
	before := cp.Available()
	for _, m := range before {
		if m.ID == "auto" {
			t.Fatal("built-in list offers auto")
		}
	}
	if len(before) == 0 {
		t.Fatal("built-in list is empty before any refresh")
	}

	ms, err := cp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("fetched %d models, want 2: %+v", len(ms), ms)
	}
	after := cp.Available()
	var ids []string
	for _, m := range after {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-4o,claude-sonnet-4" {
		t.Errorf("live list after refresh = %v", ids)
	}
	if _, ok := cp.Fetched(); !ok {
		t.Error("refresh was not remembered")
	}
}

func TestCpmFetchSessionRejected(t *testing.T) {
	acctEnv(t)
	acctWriteCopilot(t)
	cpmCopilotServer(t, http.StatusForbidden)

	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	_, err := cp.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("want re-login hint, got %v", err)
	}
	if _, ok := cp.Fetched(); ok {
		t.Error("a failed refresh must not replace the list")
	}
}
