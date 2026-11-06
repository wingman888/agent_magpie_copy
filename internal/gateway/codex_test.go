package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// codexUp stands in for the ChatGPT backend. It records what it was sent and
// replies with a script; noCType leaves Content-Type off the reply, as the
// real backend does.
type codexUp struct {
	got     map[string]any
	path    string
	head    http.Header
	reply   string
	noCType bool
	code    int
}

func (f *codexUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.got = nil
	json.Unmarshal(b, &f.got)
	f.path, f.head = r.URL.Path, r.Header
	if f.code != 0 {
		w.WriteHeader(f.code)
		io.WriteString(w, f.reply)
		return
	}
	if f.noCType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, f.reply)
}

// codexJWT is an unsigned token carrying an expiry, which is all dial reads.
func codexJWT(exp time.Time) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "email": "dev@example.com"})
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// setupCodex signs the test HOME in to Codex with an access token that expires
// at exp, points the backend at f, and returns the account provider. Token
// renewal goes to a stand-in that always refuses.
func setupCodex(t *testing.T, f *codexUp, exp time.Time) (provider.Provider, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	tok := codexJWT(exp)
	auth, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      tok,
			"access_token":  tok,
			"refresh_token": "rt-SECRET",
			"account_id":    "acct-123",
		},
	})
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}

	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = oldBase })

	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(refuse.Close)
	oldTok := provider.CodexTokenURL
	provider.CodexTokenURL = refuse.URL
	t.Cleanup(func() { provider.CodexTokenURL = oldTok })

	for _, p := range provider.Accounts() {
		if p.ID == "codex" {
			return p, tok
		}
	}
	t.Fatal("no codex account provider")
	return provider.Provider{}, ""
}

// callCodex runs the Codex Responses path directly, the way serve does for an
// account provider once the model has resolved.
func callCodex(t *testing.T, p provider.Provider, body string) (*httptest.ResponseRecorder, int, string, Usage) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	var u Usage
	code, msg := New().codexResponses(rec, req, p, "gpt-5.5", []byte(body), &u)
	return rec, code, msg, u
}

const codexReply = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":12,"output_tokens":2,"total_tokens":14}}}

`

func future() time.Time { return time.Now().Add(time.Hour) }

func TestCodexNonStreamFoldsToResponse(t *testing.T) {
	f := &codexUp{reply: codexReply}
	p, tok := setupCodex(t, f, future())
	rec, code, msg, u := callCodex(t, p, `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":100,`+
		`"temperature":0.2,"stream":false,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, msg)
	}

	// what the backend received
	if f.path != "/backend-api/codex/responses" {
		t.Errorf("path = %s", f.path)
	}
	if f.got["stream"] != true || f.got["store"] != false {
		t.Errorf("stream/store = %v/%v", f.got["stream"], f.got["store"])
	}
	if f.got["model"] != "gpt-5.5" {
		t.Errorf("model = %v", f.got["model"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := f.got[k]; ok {
			t.Errorf("%s was sent", k)
		}
	}
	if _, ok := f.got["reasoning"]; !ok {
		t.Error("reasoning dropped")
	}
	items, ok := f.got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v", f.got["input"])
	}
	if h := f.head.Get("Authorization"); h != "Bearer "+tok {
		t.Errorf("Authorization = %q", h)
	}
	if h := f.head.Get("chatgpt-account-id"); h != "acct-123" {
		t.Errorf("chatgpt-account-id = %q", h)
	}

	// what the client received
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var out struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	if out.Object != "response" {
		t.Errorf("object = %q", out.Object)
	}
	if len(out.Output) != 1 || out.Output[0].Content[0].Text != "Hello" {
		t.Errorf("output = %s", rec.Body.String())
	}
	if u.Input != 12 || u.Output != 2 {
		t.Errorf("usage = %+v", u)
	}
}

func TestCodexStreamRelaysEventsUnchanged(t *testing.T) {
	f := &codexUp{reply: codexReply}
	p, _ := setupCodex(t, f, future())
	rec, code, msg, u := callCodex(t, p, `{"model":"codex/gpt-5.5","input":[{"role":"user","content":"hi"}],`+
		`"stream":true,"max_output_tokens":50}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, msg)
	}
	if f.got["stream"] != true || f.got["store"] != false {
		t.Errorf("stream/store = %v/%v", f.got["stream"], f.got["store"])
	}
	if _, ok := f.got["max_output_tokens"]; ok {
		t.Error("max_output_tokens was sent")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Body.String() != codexReply {
		t.Errorf("events changed:\n%s", rec.Body.String())
	}
	if u.Output != 2 {
		t.Errorf("usage = %+v", u)
	}
}

func TestCodexStreamWithoutContentTypeIsStillAStream(t *testing.T) {
	for _, stream := range []string{"true", "false"} {
		t.Run("stream="+stream, func(t *testing.T) {
			f := &codexUp{reply: codexReply, noCType: true}
			p, _ := setupCodex(t, f, future())
			rec, code, msg, _ := callCodex(t, p, `{"model":"codex/gpt-5.5","input":"x","stream":`+stream+`}`)
			if code != 200 {
				t.Fatalf("status %d: %s", code, msg)
			}
			if stream == "true" {
				if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
					t.Errorf("Content-Type = %q", ct)
				}
				if rec.Body.String() != codexReply {
					t.Errorf("events changed:\n%s", rec.Body.String())
				}
				return
			}
			if !strings.Contains(rec.Body.String(), `"object":"response"`) || !strings.Contains(rec.Body.String(), "Hello") {
				t.Errorf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestCodexSignedOutAsksForSignIn(t *testing.T) {
	f := &codexUp{reply: codexReply}
	p, _ := setupCodex(t, f, future())
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	rec, code, msg, _ := callCodex(t, p, `{"model":"codex/gpt-5.5","input":"x"}`)
	if code != http.StatusUnauthorized || !strings.Contains(msg, `run "codex login" again`) {
		t.Fatalf("status %d, %q", code, msg)
	}
	if !strings.Contains(rec.Body.String(), `codex login`) {
		t.Errorf("client body = %s", rec.Body.String())
	}
}

func TestCodexBackend401AsksForSignIn(t *testing.T) {
	f := &codexUp{code: http.StatusUnauthorized, reply: `{"error":{"message":"token revoked"}}`}
	p, _ := setupCodex(t, f, future())
	_, code, msg, _ := callCodex(t, p, `{"model":"codex/gpt-5.5","input":"x"}`)
	if code != http.StatusUnauthorized || msg != provider.CodexSignInExpired.Error() {
		t.Fatalf("status %d, %q", code, msg)
	}
}

func TestCodexFailedRenewalAsksForSignIn(t *testing.T) {
	f := &codexUp{reply: codexReply}
	p, _ := setupCodex(t, f, time.Now().Add(-time.Minute))
	_, code, msg, _ := callCodex(t, p, `{"model":"codex/gpt-5.5","input":"x"}`)
	if code != http.StatusUnauthorized || !strings.Contains(msg, `run "codex login" again`) {
		t.Fatalf("status %d, %q", code, msg)
	}
	if f.got != nil {
		t.Error("request reached the backend with an expired token")
	}
}
