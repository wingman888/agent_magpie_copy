package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// gwAcctUp is an upstream that records what it was sent and answers with a
// fixed reply. ctype "" sends no Content-Type at all.
type gwAcctUp struct {
	path  string
	head  http.Header
	body  []byte
	reply string
	ctype string
	code  int
}

func (u *gwAcctUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.path, u.head = r.URL.Path, r.Header.Clone()
	u.body, _ = io.ReadAll(r.Body)
	if u.ctype == "" {
		w.Header()["Content-Type"] = nil // stop net/http from sniffing one
	} else {
		w.Header().Set("Content-Type", u.ctype)
	}
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.reply)
}

// signedJWT is an unsigned JWT with the given claims; the gateway only reads them.
func gwAcctJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// gwAcctLogin signs Codex in with a ChatGPT account under a fresh HOME. The
// access token expires at exp. It returns the access token and the login file.
func gwAcctLogin(t *testing.T, exp int64) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	oldBase, oldRefresh := provider.CodexBase, provider.CodexRefreshURL
	t.Cleanup(func() { provider.CodexBase, provider.CodexRefreshURL = oldBase, oldRefresh })

	access := gwAcctJWT(map[string]any{"exp": exp})
	id := gwAcctJWT(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	body, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"extra":     "keep",
		"tokens": map[string]any{
			"id_token": id, "access_token": access, "refresh_token": "rt-old", "account_id": "acct-1",
		},
	})
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return access, path
}

// gwAcctSSE joins lines into an SSE body (a private copy of the package helper).
func gwAcctSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// gwAcctReply is a full Responses stream that says "Hello".
var gwAcctReply = gwAcctSSE(
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","status":"in_progress"}}`,
	`data: {"type":"response.output_text.delta","delta":"Hello"}`,
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}`,
)

// serveCodex points the Codex backend at a fake and returns the fake.
func gwAcctServe(t *testing.T, up *gwAcctUp) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL + "/backend-api/codex"
}

// postRec sends one request through a fresh gateway and keeps the recorder.
func gwAcctPost(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec
}

func gwAcctSentJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("upstream body is not JSON: %v: %s", err, b)
	}
	return m
}

func TestGwAcctCodexBodyShape(t *testing.T) {
	got := gwAcctSentJSON(t, codexBody([]byte(`{"model":"codex/gpt-5.5","input":"hi","stream":false,"store":true,"temperature":1,"top_p":1,"max_output_tokens":9,"max_tokens":9,"tools":[]}`)))
	if got["model"] != "gpt-5.5" || got["stream"] != true || got["store"] != false {
		t.Errorf("model/stream/store: %v", got)
	}
	for _, k := range []string{"temperature", "top_p", "max_output_tokens", "max_tokens"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be dropped: %v", k, got)
		}
	}
	in, ok := got["input"].([]any)
	if !ok || len(in) != 1 || in[0].(map[string]any)["content"] != "hi" || in[0].(map[string]any)["role"] != "user" {
		t.Errorf("string input not turned into a list: %v", got["input"])
	}
	// a list input is left as a list, and a bare model name is left alone
	got = gwAcctSentJSON(t, codexBody([]byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"x"}]}`)))
	if got["model"] != "gpt-5.5" {
		t.Errorf("model = %v", got["model"])
	}
	if in, ok := got["input"].([]any); !ok || len(in) != 1 {
		t.Errorf("list input changed: %v", got["input"])
	}
}

func TestGwAcctCodexNonStreamReturnsWholeReply(t *testing.T) {
	access, _ := gwAcctLogin(t, time.Now().Add(time.Hour).Unix())
	up := &gwAcctUp{reply: gwAcctReply, ctype: "text/event-stream"}
	gwAcctServe(t, up)

	rec := gwAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","temperature":0.2,"max_output_tokens":50}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("client reply is not one JSON document: %v: %s", err, rec.Body)
	}
	if got["object"] != "response" || got["status"] != "completed" || !strings.Contains(rec.Body.String(), `"text":"Hello"`) {
		t.Errorf("client reply: %s", rec.Body)
	}

	if !strings.HasSuffix(up.path, "/responses") {
		t.Errorf("upstream path = %s", up.path)
	}
	sent := gwAcctSentJSON(t, up.body)
	if sent["stream"] != true || sent["store"] != false || sent["model"] != "gpt-5.5" {
		t.Errorf("upstream stream/store/model: %v", sent)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("upstream got %s: %v", k, sent)
		}
	}
	in, ok := sent["input"].([]any)
	if !ok || len(in) != 1 || in[0].(map[string]any)["content"] != "hi" {
		t.Errorf("upstream input: %v", sent["input"])
	}
	if up.head.Get("Authorization") != "Bearer "+access || up.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("upstream auth headers: %v", up.head)
	}
}

func TestGwAcctCodexStreamWithoutContentType(t *testing.T) {
	gwAcctLogin(t, time.Now().Add(time.Hour).Unix())
	up := &gwAcctUp{reply: gwAcctReply} // no Content-Type at all
	gwAcctServe(t, up)

	rec := gwAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":[{"role":"user","content":"hi"}],"stream":true,"top_p":0.9}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("client Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body)
	}
	sent := gwAcctSentJSON(t, up.body)
	if sent["stream"] != true {
		t.Errorf("stream not forced on: %v", sent)
	}
	if _, ok := sent["top_p"]; ok {
		t.Errorf("top_p reached the upstream: %v", sent)
	}
}

func TestGwAcctCodexRefreshFailureAsksForLogin(t *testing.T) {
	gwAcctLogin(t, time.Now().Add(-time.Hour).Unix()) // already expired
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(refresh.Close)
	provider.CodexRefreshURL = refresh.URL
	up := &gwAcctUp{reply: gwAcctReply, ctype: "text/event-stream"}
	gwAcctServe(t, up)

	rec := gwAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != 502 {
		t.Errorf("status %d, want 502: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "已登出，请运行 `codex login`") {
		t.Errorf("message does not say to sign in again: %s", rec.Body)
	}
	if up.path != "" {
		t.Errorf("upstream was called with a dead sign-in: %s", up.path)
	}
}

func TestGwAcctCodexRefreshWritesBackAndKeepsOtherFields(t *testing.T) {
	_, file := gwAcctLogin(t, time.Now().Add(-time.Hour).Unix())
	newAccess := gwAcctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var sentRefresh map[string]any
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sentRefresh)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	t.Cleanup(refresh.Close)
	provider.CodexRefreshURL = refresh.URL
	up := &gwAcctUp{reply: gwAcctReply, ctype: "text/event-stream"}
	gwAcctServe(t, up)

	rec := gwAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if sentRefresh["grant_type"] != "refresh_token" || sentRefresh["refresh_token"] != "rt-old" {
		t.Errorf("refresh request: %v", sentRefresh)
	}
	if up.head.Get("Authorization") != "Bearer "+newAccess {
		t.Errorf("upstream got the old token: %v", up.head.Get("Authorization"))
	}

	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Extra       string `json:"extra"`
		AuthMode    string `json:"auth_mode"`
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Tokens.AccessToken != newAccess || f.Tokens.RefreshToken != "rt-new" {
		t.Errorf("tokens not written back: %+v", f.Tokens)
	}
	if f.Tokens.IDToken == "" || f.Tokens.AccountID != "acct-1" || f.Extra != "keep" || f.AuthMode != "chatgpt" {
		t.Errorf("other fields lost: %s", b)
	}
	if f.LastRefresh == "" {
		t.Errorf("last_refresh not set: %s", b)
	}
}

// copilotServer signs Copilot in and serves its token exchange and chat API
// from one fake. tokenStatus is the answer to the exchange; api receives chat.
func gwAcctCopilotServer(t *testing.T, tokenStatus int, api *gwAcctUp) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(xdg, "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:1":{"user":"octo","oauth_token":"gh-oauth"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTok := provider.CopilotTokenURL
	t.Cleanup(func() { provider.CopilotTokenURL = oldTok })

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenStatus != http.StatusOK || r.Header.Get("Authorization") != "token gh-oauth" {
			w.WriteHeader(tokenStatus)
			return
		}
		fmt.Fprintf(w, `{"token":"sess-1","endpoints":{"api":%q}}`, srv.URL+"/api")
	})
	mux.Handle("/api/", api)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	provider.CopilotTokenURL = srv.URL + "/token"
}

func TestGwAcctCopilotUsesSessionAndTurnHeaders(t *testing.T) {
	api := &gwAcctUp{ctype: "application/json", reply: `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"a cat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`}
	gwAcctCopilotServer(t, http.StatusOK, api)

	body := `{"model":"copilot/gpt-4o","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"look","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"a cat"}]}`
	rec := gwAcctPost(t, "/v1/chat/completions", body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "a cat") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if api.path != "/api/chat/completions" {
		t.Errorf("upstream path = %s", api.path)
	}
	h := api.head
	if h.Get("Authorization") != "Bearer sess-1" {
		t.Errorf("Authorization = %q, want the session token", h.Get("Authorization"))
	}
	if h.Get("X-Initiator") != "agent" {
		t.Errorf("X-Initiator = %q, want agent (the last turn is a tool result)", h.Get("X-Initiator"))
	}
	if h.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("Copilot-Vision-Request = %q, want true", h.Get("Copilot-Vision-Request"))
	}
	if h.Get("Copilot-Integration-Id") != "vscode-chat" {
		t.Errorf("Copilot-Integration-Id = %q", h.Get("Copilot-Integration-Id"))
	}
	if sent := gwAcctSentJSON(t, api.body); sent["model"] != "gpt-4o" {
		t.Errorf("upstream model = %v", sent["model"])
	}
}

func TestGwAcctCopilotRejectedSessionAsksForLogin(t *testing.T) {
	api := &gwAcctUp{reply: `{}`, ctype: "application/json"}
	gwAcctCopilotServer(t, http.StatusUnauthorized, api)

	rec := gwAcctPost(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 502 {
		t.Errorf("status %d, want 502: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "请重新登录 Copilot") {
		t.Errorf("message does not say to sign in again: %s", rec.Body)
	}
	if api.path != "" {
		t.Errorf("chat was called without a session: %s", api.path)
	}
}

// sniffEventStream must look past leading blank lines (a stream may open with
// \r\n\r\n) and must hand the whole body on to the reader afterwards.
func TestGwAcctSniffEventStreamSkipsLeadingBlankLines(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"\r\n\r\nevent: response.created\r\ndata: {}\r\n\r\n", true},
		{"\n  data: {\"type\":\"x\"}\n\n", true},
		{"\r\n\r\n\r\n", false},
		{`{"error":"nope"}`, false},
	}
	for _, c := range cases {
		// one byte per read, so the sniff sees the body as it trickles in
		res := &http.Response{Header: http.Header{}, Body: io.NopCloser(iotest.OneByteReader(strings.NewReader(c.body)))}
		sniffEventStream(res)
		got := res.Header.Get("Content-Type") == "text/event-stream"
		if got != c.want {
			t.Errorf("sniff(%q) = %v, want %v", c.body, got, c.want)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil || string(b) != c.body {
			t.Errorf("body after sniff = %q (%v), want %q", b, err, c.body)
		}
	}
}
