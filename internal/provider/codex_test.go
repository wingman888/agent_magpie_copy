package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// expiringCodexLogin writes a ChatGPT login whose access token runs out in a
// minute, so the next use must refresh it. It returns the access token.
func expiringCodexLogin(t *testing.T) string {
	t.Helper()
	isolate(t)
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "2020-01-01T00:00:00Z",
		"extra":          map[string]any{"keep": true},
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com"}),
			"access_token":  access,
			"refresh_token": "rt-old",
			"account_id":    "acct-1",
		},
	})
	put(t, codexFile(), string(b))
	return access
}

// fakeAuth is a token endpoint that answers with the given status and body,
// and records the request it was sent.
func fakeAuth(t *testing.T, code int, reply string) (*httptest.Server, *map[string]string) {
	t.Helper()
	got := &map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := map[string]string{}
		json.NewDecoder(r.Body).Decode(&m)
		*got = m
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	old := CodexAuthURL
	CodexAuthURL = srv.URL
	t.Cleanup(func() { CodexAuthURL = old })
	return srv, got
}

func TestCodexBodyRewrite(t *testing.T) {
	out := CodexBody([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":64,"temperature":0.2,"top_p":0.9,"stream":false,"store":true,"instructions":"be brief"}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false {
		t.Errorf("model/stream/store = %v/%v/%v", m["model"], m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was sent upstream", k)
		}
	}
	if m["instructions"] != "be brief" {
		t.Errorf("instructions lost: %v", m["instructions"])
	}
	in, ok := m["input"].([]any)
	if !ok || len(in) != 1 {
		t.Fatalf("input is not a one-item list: %v", m["input"])
	}
	item, _ := in[0].(map[string]any)
	content, _ := item["content"].([]any)
	part, _ := content[0].(map[string]any)
	if item["role"] != "user" || part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("string input became %v", in)
	}
}

func TestCodexRefreshWritesBackAndKeepsOtherFields(t *testing.T) {
	expiringCodexLogin(t)
	newAccess := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	_, got := fakeAuth(t, 200, `{"access_token":"`+newAccess+`","refresh_token":"rt-new"}`)

	tok, acct, err := codexAccess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != newAccess || acct != "acct-1" {
		t.Fatalf("token %q account %q", tok, acct)
	}
	if (*got)["grant_type"] != "refresh_token" || (*got)["refresh_token"] != "rt-old" || (*got)["client_id"] != codexClientID {
		t.Errorf("refresh request = %v", *got)
	}
	raw, err := os.ReadFile(codexFile())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		AuthMode    string `json:"auth_mode"`
		LastRefresh string `json:"last_refresh"`
		Extra       struct {
			Keep bool `json:"keep"`
		} `json:"extra"`
		Tokens struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Tokens.AccessToken != newAccess || f.Tokens.RefreshToken != "rt-new" {
		t.Errorf("tokens not written back: %+v", f.Tokens)
	}
	if f.Tokens.AccountID != "acct-1" || f.Tokens.IDToken == "" {
		t.Errorf("token fields lost: %+v", f.Tokens)
	}
	if f.AuthMode != "chatgpt" || !f.Extra.Keep || !strings.Contains(string(raw), `"OPENAI_API_KEY"`) {
		t.Errorf("other fields lost: %s", raw)
	}
	if f.LastRefresh == "" || f.LastRefresh == "2020-01-01T00:00:00Z" {
		t.Errorf("last_refresh not updated: %q", f.LastRefresh)
	}
}

func TestCodexRefreshFailureSaysLogIn(t *testing.T) {
	expiringCodexLogin(t)
	fakeAuth(t, 400, `{"error":"invalid_grant"}`)
	_, _, err := codexAccess(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v, want a message telling the user to run codex login", err)
	}
}

func TestCodexWithoutLoginIsLoggedOut(t *testing.T) {
	isolate(t)
	if _, _, err := codexAccess(context.Background()); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountRejectedOnlyForAgents(t *testing.T) {
	codex := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	if err := AccountRejected(codex, http.StatusUnauthorized); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("codex 401 -> %v", err)
	}
	if err := AccountRejected(codex, http.StatusBadRequest); err != nil {
		t.Errorf("codex 400 -> %v, want nil", err)
	}
	keyed := Provider{ID: "x", Key: "k"}
	if err := AccountRejected(keyed, http.StatusUnauthorized); err != nil {
		t.Errorf("key provider 401 -> %v, want nil", err)
	}
}

func TestCopilotHeadersMarkTheTurn(t *testing.T) {
	user := copilotHeaders([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`))
	if user["X-Initiator"] != "user" || user["Copilot-Vision-Request"] != "" {
		t.Errorf("user turn headers = %v", user)
	}
	tool := copilotHeaders([]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null},{"role":"tool","tool_call_id":"c","content":"r"}]}`))
	if tool["X-Initiator"] != "agent" {
		t.Errorf("tool turn headers = %v", tool)
	}
	vision := copilotHeaders([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"what"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`))
	if vision["Copilot-Vision-Request"] != "true" || vision["X-Initiator"] != "user" {
		t.Errorf("vision headers = %v", vision)
	}
}
