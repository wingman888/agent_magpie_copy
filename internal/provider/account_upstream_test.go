package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupUpstream is setupAgents plus the refresh and token URLs restored after
// each test.
func setupUpstream(t *testing.T) string {
	t.Helper()
	home := setupAgents(t)
	oldRefresh, oldToken := CodexRefreshURL, CopilotTokenURL
	t.Cleanup(func() { CodexRefreshURL, CopilotTokenURL = oldRefresh, oldToken })
	return home
}

// writeExpiringCodexLogin writes a ChatGPT login whose access token expires
// in the given duration, with an unrelated top-level field and an unrelated
// token field that dial must keep.
func writeExpiringCodexLogin(t *testing.T, home string, in time.Duration) {
	t.Helper()
	idTok := jwt(map[string]any{"email": "dev@example.com"})
	accTok := jwt(map[string]any{"exp": time.Now().Add(in).Unix()})
	body := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"custom":{"keep":[1,2]},"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"rt-old","account_id":"acct-1","extra":"kept"}}`,
		idTok, accTok)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), body)
}

func codexProvider() Provider {
	return Provider{ID: CodexID, Name: "Codex", Account: &Account{User: "dev@example.com", Agent: "Codex"}}
}

func TestCodexFreshTokenIsNotRefreshed(t *testing.T) {
	home := setupUpstream(t)
	writeExpiringCodexLogin(t, home, time.Hour)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	CodexRefreshURL = srv.URL

	up, err := codexProvider().AccountUpstream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("refresh called %d times for a fresh token", hits)
	}
	if up.Base != CodexBase || !strings.HasPrefix(up.Headers["Authorization"], "Bearer ") {
		t.Errorf("upstream %+v", up)
	}
	if up.Headers["chatgpt-account-id"] != "acct-1" {
		t.Errorf("account header %q", up.Headers["chatgpt-account-id"])
	}
}

func TestCodexNearExpiryRefreshesAndWritesBack(t *testing.T) {
	home := setupUpstream(t)
	writeExpiringCodexLogin(t, home, time.Minute)
	newAccess := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})

	var sent map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt-new"}`, newAccess)
	}))
	defer srv.Close()
	CodexRefreshURL = srv.URL

	up, err := codexProvider().AccountUpstream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "rt-old" || sent["client_id"] != codexClientID {
		t.Errorf("refresh request %v", sent)
	}
	if up.Headers["Authorization"] != "Bearer "+newAccess {
		t.Errorf("request used the old token")
	}

	raw := mustRead(t, filepath.Join(home, ".codex", "auth.json"))
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	tokens := file["tokens"].(map[string]any)
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "rt-new" {
		t.Errorf("tokens not written back: %v", tokens)
	}
	if tokens["id_token"] == nil || tokens["account_id"] != "acct-1" || tokens["extra"] != "kept" {
		t.Errorf("token fields lost: %v", tokens)
	}
	if _, ok := file["OPENAI_API_KEY"]; !ok {
		t.Errorf("top-level OPENAI_API_KEY lost")
	}
	if file["auth_mode"] != "chatgpt" {
		t.Errorf("auth_mode lost: %v", file["auth_mode"])
	}
	custom, _ := file["custom"].(map[string]any)
	if custom == nil || len(custom["keep"].([]any)) != 2 {
		t.Errorf("nested field lost: %v", file["custom"])
	}
	if info, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("auth file mode after write: %v %v", info, err)
	}
}

func TestCodexRefreshFailureSaysSignedOut(t *testing.T) {
	home := setupUpstream(t)
	writeExpiringCodexLogin(t, home, time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()
	CodexRefreshURL = srv.URL

	_, err := codexProvider().AccountUpstream(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("want a sign-in hint, got %v", err)
	}
	// a failed refresh must not touch the login file
	if !strings.Contains(string(mustRead(t, filepath.Join(home, ".codex", "auth.json"))), `"rt-old"`) {
		t.Errorf("login file changed after failed refresh")
	}
}

func TestCodexBodyShape(t *testing.T) {
	in := `{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":16,"temperature":0.2,"top_p":0.9,"max_completion_tokens":5,"store":true,"instructions":"be brief"}`
	out, err := CodexBody([]byte(in), "")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false {
		t.Errorf("model/stream/store: %v", m)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "max_completion_tokens"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was not removed", k)
		}
	}
	if m["instructions"] != "be brief" {
		t.Errorf("unrelated field changed: %v", m["instructions"])
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input not a list: %v", m["input"])
	}
	item := items[0].(map[string]any)
	part := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || part["type"] != "input_text" || part["text"] != "hello" {
		t.Errorf("input item: %v", item)
	}
}

func TestCopilotUpstreamUsesSessionAPI(t *testing.T) {
	setupUpstream(t)
	writeCopilotLogin(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.Header.Get("Authorization") != "token gho_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"token":"sess-2","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL+"/api/")
		}
	}))
	defer srv.Close()
	CopilotTokenURL = srv.URL + "/token"

	up, err := Provider{ID: CopilotID, Account: &Account{User: "octo", Agent: "Copilot"}}.AccountUpstream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if up.Base != srv.URL+"/api" {
		t.Errorf("base %q, want the session's API address", up.Base)
	}
	if up.Headers["Authorization"] != "Bearer sess-2" {
		t.Errorf("auth %q", up.Headers["Authorization"])
	}
}

func TestCopilotSignedOutUpstream(t *testing.T) {
	setupUpstream(t)
	writeCopilotLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	CopilotTokenURL = srv.URL

	_, err := Provider{ID: CopilotID}.AccountUpstream(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("want a re-login hint, got %v", err)
	}
}

func TestCopilotHeaders(t *testing.T) {
	cases := []struct {
		name, body, initiator, vision string
	}{
		{"user turn", `{"messages":[{"role":"user","content":"hi"}]}`, "user", ""},
		{"tool result", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"1"}]},{"role":"tool","tool_call_id":"1","content":"ok"}]}`, "agent", ""},
		{"assistant tool call last", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"1"}]}]}`, "agent", ""},
		{"image", `{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, "user", "true"},
	}
	for _, c := range cases {
		h := CopilotHeaders([]byte(c.body))
		if h["X-Initiator"] != c.initiator {
			t.Errorf("%s: X-Initiator %q", c.name, h["X-Initiator"])
		}
		if h["Copilot-Vision-Request"] != c.vision {
			t.Errorf("%s: vision %q", c.name, h["Copilot-Vision-Request"])
		}
	}
}

func TestCodexTestProbeUsesStreamedBody(t *testing.T) {
	home := setupUpstream(t)
	writeExpiringCodexLogin(t, home, time.Hour)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {}\n\n")
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL
	defer func() { CodexBase = old }()

	p := codexProvider()
	p.Responses = CodexBase // as accountProvider sets it, so the probe has an endpoint
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("probe %+v", res)
	}
	if !strings.Contains(got, `"stream":true`) || strings.Contains(got, "max_output_tokens") {
		t.Errorf("probe body %s", got)
	}
}
