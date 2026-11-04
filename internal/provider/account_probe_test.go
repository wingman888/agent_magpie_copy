package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A signed-in account is probed the way its agent calls the backend: the
// request carries the agent's token and account id, and its body is shaped
// for the backend. Whatever address or key was typed in is never used.
func TestAccountTestSignsRequest(t *testing.T) {
	home := acctIsolate(t)
	access := acctWriteCodexLogin(t, home, time.Now().Add(time.Hour))

	var gotAuth, gotAcct, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("chatgpt-account-id")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {}\n\n")
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL
	t.Cleanup(func() { CodexBase = old })

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Chat: "https://ignored.example/v1", Key: "sk-ignored"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("Test() = %+v, want one OK Responses probe", res)
	}
	if gotPath != "/responses" {
		t.Errorf("request path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer "+access {
		t.Errorf("Authorization = %q, want the agent's access token", gotAuth)
	}
	if gotAcct != "acct-1" {
		t.Errorf("chatgpt-account-id = %q, want acct-1", gotAcct)
	}
	if gotBody["stream"] != true || gotBody["store"] != false {
		t.Errorf("body stream/store = %v/%v, want true/false", gotBody["stream"], gotBody["store"])
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens was sent to the backend: %v", gotBody)
	}
	if gotBody["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5", gotBody["model"])
	}
	if _, ok := gotBody["input"].([]any); !ok {
		t.Errorf("input = %v, want a list of items", gotBody["input"])
	}
}

// Without a login the probe reports the sign-out instead of sending anything.
func TestAccountTestReportsSignedOut(t *testing.T) {
	acctIsolate(t)
	p := Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex", User: "x"}, Models: []string{"gpt-5.5"}}
	res := p.Test(context.Background())
	if len(res) != 1 || res[0].OK || res[0].Error == "" {
		t.Fatalf("Test() = %+v, want one failed probe with a message", res)
	}
}
