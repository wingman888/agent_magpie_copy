package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A chat stream that opens with a rate-limit error is retried on the fallback
// before anything reaches the client, as the Chat-to-Chat relay path does.
func TestPassthroughStreamOpeningErrorMovesToFallback(t *testing.T) {
	prim := &scripted{streamErr: map[string]bool{"k1": true}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	body := `{"model":"prim/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := postTo(New().Handler(), "/v1/chat/completions", body)
	out := rec.Body.String()
	if rec.Code != 200 || strings.Contains(out, "rate limit") || !strings.Contains(out, "hi") {
		t.Fatalf("status %d, body %q; want the fallback's reply only", rec.Code, out)
	}
	if bm, bk := back.took(); strings.Join(bm, ",") != "m2" || strings.Join(bk, ",") != "k2" {
		t.Fatalf("fallback sent %v with %v", bm, bk)
	}
}

// Once a relayed stream has written content, a later error is not retried:
// the client sees it as it comes, and no other candidate is tried.
func TestPassthroughStartedStreamIsNotRetried(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"error":{"message":"rate limit exceeded"}}`+"\n\n")
	}))
	t.Cleanup(partial.Close)
	back := &scripted{}
	bu := httptest.NewServer(back)
	t.Cleanup(bu.Close)
	if err := provider.Save(provider.Provider{ID: "prim", Name: "Prim", Key: "k1", Models: []string{"m1"}, Chat: partial.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "back", Name: "Back", Key: "k2", Models: []string{"m2"}, Chat: bu.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetFallback("prim", []string{"back/m2"}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"prim/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := postTo(New().Handler(), "/v1/chat/completions", body)
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("body %q: the partial content must reach the client", rec.Body.String())
	}
	if bm, _ := back.took(); len(bm) != 0 {
		t.Fatalf("fallback was used after content was written: %v", bm)
	}
}

// fakeClaudeCLI puts a stand-in `claude` first on PATH. It reports one signed-in
// account, and runs a turn as the account whose OAuth token it is given: the
// sign-in in use (no token) is rate-limited, the account with other-tok answers.
func fakeClaudeCLI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo '{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}'
  exit 0
fi
cat > /dev/null
if [ "$CLAUDE_CODE_OAUTH_TOKEN" != "other-tok" ]; then
  echo '{"type":"result","subtype":"error","is_error":true,"result":"API Error: 429 rate limit exceeded"}'
  exit 0
fi
echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":0}}}}'
echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from other"}}}'
echo '{"type":"stream_event","event":{"type":"message_stop"}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"from other"}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// claudeHome signs Claude Code in as me@example.com, and magpie remembers a
// second account, other@example.com, that works alongside it.
func claudeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	future := time.Now().Add(time.Hour).UnixMilli()
	write := func(path string, v any) {
		b, _ := json.Marshal(v)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "own-tok", "refreshToken": "own-r", "expiresAt": future, "subscriptionType": "max"}})
	write(filepath.Join(filepath.Dir(provider.Path()), "accounts.json"), map[string]any{"accounts": []map[string]any{{
		"agent": "claude", "user": "other@example.com", "plan": "max", "on": true,
		"creds": map[string]any{"accessToken": "other-tok", "refreshToken": "other-r", "expiresAt": future, "subscriptionType": "max"},
	}}})
	provider.ResetClaudeCache()
	t.Cleanup(provider.ResetClaudeCache)
}

// A Claude Code subscription that is rate-limited hands the request to the
// remembered account that works alongside it, whether the client streams or not.
func TestClaudeAlongsideAccountTakesOver(t *testing.T) {
	fakeClaudeCLI(t)
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "stream"}[stream], func(t *testing.T) {
			claudeHome(t)
			srv := New()
			body := `{"model":"claude/claude-sonnet-5","max_tokens":10,"stream":` + map[bool]string{false: "false", true: "true"}[stream] +
				`,"messages":[{"role":"user","content":"hi"}]}`
			rec := postTo(srv.Handler(), "/v1/messages", body)
			out := rec.Body.String()
			if rec.Code != 200 || !strings.Contains(out, "from other") || strings.Contains(out, "rate limit") {
				t.Fatalf("status %d, body %q; want the other account's reply only", rec.Code, out)
			}
			calls := srv.Recent()
			if len(calls) == 0 || calls[0].Provider != "claude" || calls[0].Fallback == "" {
				t.Fatalf("call record = %+v; want the claude provider with the failed sign-in named", calls)
			}
		})
	}
}

// Words that merely contain a status code or a vague term are not failures.
func TestStreamFailureIgnoresIncidentalWords(t *testing.T) {
	cases := []struct {
		payload string
		want    bool
	}{
		{`{"error":{"message":"request 4290 tokens is too long"}}`, false},
		{`{"error":{"message":"parameter unavailable"}}`, false},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"rate limit wording in a bad request"}}`, false},
		{`{"error":{"code":400,"message":"quota field malformed"}}`, false},
		{`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, true},
		{`{"error":{"type":"insufficient_quota","message":"out of credit"}}`, true},
		{`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, true},
		{`{"error":{"status":"RESOURCE_EXHAUSTED","message":"x"}}`, true},
		{`API Error: 429 rate limit exceeded`, true},
		{`service unavailable`, true},
	}
	for _, c := range cases {
		if got := streamFailure(c.payload); got != c.want {
			t.Errorf("streamFailure(%q) = %v, want %v", c.payload, got, c.want)
		}
	}
}
