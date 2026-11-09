package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// s2Body decodes a JSON object for field checks.
func s2Body(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, b)
	}
	return m
}

func TestS2CodexBodyRewrite(t *testing.T) {
	in := []byte(`{"model":"codex/gpt-5.5","input":"hi","temperature":0.2,"top_p":0.9,"max_output_tokens":64,"stream":false,"store":true}`)
	out, err := AdaptCodexBody(in)
	if err != nil {
		t.Fatal(err)
	}
	m := s2Body(t, out)
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must be removed: %s", k, out)
		}
	}
	list, ok := m["input"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("string input must become a list: %s", out)
	}
	item := list[0].(map[string]any)
	if item["role"] != "user" || item["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Errorf("input item = %v", item)
	}
}

func TestS2CodexBodyKeepsListInput(t *testing.T) {
	in := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[]}]}`)
	out, err := AdaptCodexBody(in)
	if err != nil {
		t.Fatal(err)
	}
	if list, ok := s2Body(t, out)["input"].([]any); !ok || len(list) != 1 {
		t.Errorf("list input changed: %s", out)
	}
}

func TestS2CopilotChatModelFilter(t *testing.T) {
	f := false
	tr := true
	rows := []copilotModel{
		{ID: "auto"},
		{ID: "gpt-5.5", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}, ModelPickerEnabled: &tr},
		{ID: "text-embedding-3", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "embeddings"}},
		{ID: "gpt-internal-x", ModelPickerCategory: "internal"},
		{ID: "claude-off", ModelPickerEnabled: &f},
		{ID: "gpt-5.5"}, // duplicate
		{ID: "gemini-pro", Policy: &struct {
			State string `json:"state"`
		}{State: "disabled"}},
		{ID: "claude-sonnet", Name: "Claude Sonnet", ModelPickerEnabled: &tr},
	}
	got := chatModels(rows)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-5.5,claude-sonnet" {
		t.Errorf("filtered = %v", ids)
	}
	if got[1].Name != "Claude Sonnet" {
		t.Errorf("name = %q", got[1].Name)
	}
}

func TestS2CopilotInitiatorAndVision(t *testing.T) {
	user := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`)
	if copilotInitiator(user) != "user" || !copilotHasImage(user) {
		t.Errorf("user turn with image: initiator=%s", copilotInitiator(user))
	}
	tool := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null},{"role":"tool","content":"42"}]}`)
	if copilotInitiator(tool) != "agent" || copilotHasImage(tool) {
		t.Errorf("tool turn: initiator=%s", copilotInitiator(tool))
	}
	h := copilotHeaders("tok", tool)
	if h["X-Initiator"] != "agent" || h["Authorization"] != "Bearer tok" {
		t.Errorf("headers = %v", h)
	}
	if _, ok := h["Copilot-Vision-Request"]; ok {
		t.Error("vision header on a text-only request")
	}
	if copilotHeaders("tok", user)["Copilot-Vision-Request"] != "true" {
		t.Error("image request must be marked as vision")
	}
}

func TestS2CopilotRefreshSavesLiveList(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, home+"/xdgconfig/github-copilot/apps.json",
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
	var seenAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"auto","capabilities":{"type":"chat"}},{"id":"gpt-5.5","name":"GPT 5.5","capabilities":{"type":"chat"}},{"id":"text-embedding","capabilities":{"type":"embeddings"}}]}`))
	}))
	defer api.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"token":"sess_1","endpoints":{"api":"` + api.URL + `"}}`))
	}))
	defer tokenSrv.Close()
	old := copilotTokenURL
	copilotTokenURL = tokenSrv.URL
	t.Cleanup(func() { copilotTokenURL = old })

	p, ok := acctFind(All(), "copilot")
	if !ok {
		t.Fatal("copilot not listed")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("refreshed list = %+v", ms)
	}
	if seenAuth != "Bearer sess_1" {
		t.Errorf("models request auth = %q", seenAuth)
	}
	live, _, ok := catalog.Live("copilot")
	if !ok || len(live) != 1 || live[0].ID != "gpt-5.5" {
		t.Errorf("live list not saved: %+v", live)
	}
	p2, _ := acctFind(All(), "copilot")
	if got := p2.Available(); len(got) != 1 || got[0].ID != "gpt-5.5" {
		t.Errorf("available after refresh = %+v", got)
	}
}

func TestS2CopilotSessionRefusedSaysSignIn(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, home+"/xdgconfig/github-copilot/apps.json",
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_old"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := copilotTokenURL
	copilotTokenURL = srv.URL
	t.Cleanup(func() { copilotTokenURL = old })
	_, _, _, err := AccountUpstream(context.Background(), Provider{ID: copilotID, Account: &Account{User: "octo"}}, Chat, []byte(`{"messages":[]}`))
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("expected sign-in hint, got %v", err)
	}
}

func TestS2CodexUpstreamHeaders(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))
	base, h, body, err := AccountUpstream(context.Background(), Provider{ID: codexID, Responses: CodexBase}, Responses, []byte(`{"model":"codex/gpt-5.5","input":"x","temperature":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if base != CodexBase {
		t.Errorf("base = %s", base)
	}
	if h["Authorization"] == "" || !strings.HasPrefix(h["Authorization"], "Bearer ") {
		t.Errorf("auth header = %v", h)
	}
	if h["chatgpt-account-id"] == "" {
		t.Errorf("account id header missing: %v", h)
	}
	if strings.Contains(string(body), "temperature") {
		t.Errorf("body keeps temperature: %s", body)
	}
}
