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

// s6Backend stands in for the ChatGPT backend behind a Codex sign-in.
type s6Backend struct {
	got    map[string]any
	hits   int
	auth   string
	acct   string
	reply  string
	noType bool // leave out Content-Type, as the ChatGPT backend does
}

func (b *s6Backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.hits++
	b.got = nil
	json.Unmarshal(raw, &b.got)
	b.auth = r.Header.Get("Authorization")
	b.acct = r.Header.Get("chatgpt-account-id")
	if b.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, b.reply)
}

func s6JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s6Setup signs Codex in (ChatGPT mode, token valid for an hour), points the
// backend at b, and returns the access token.
func s6Setup(t *testing.T, b *s6Backend) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	access := s6JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      s6JWT(map[string]any{"email": "dev@example.com"}),
			"access_token":  access,
			"refresh_token": "ref-s6",
			"account_id":    "acct-s6",
		},
	}
	raw, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	return access
}

func s6Run(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	return rec
}

// s6CheckUpstream asserts what the ChatGPT backend received.
func s6CheckUpstream(t *testing.T, b *s6Backend, access string) {
	t.Helper()
	if b.hits != 1 {
		t.Fatalf("backend hit %d times; want 1", b.hits)
	}
	if b.got == nil {
		t.Fatal("backend received no JSON body")
	}
	if b.got["stream"] != true {
		t.Errorf("stream = %v; want true", b.got["stream"])
	}
	if b.got["store"] != false {
		t.Errorf("store = %v; want false", b.got["store"])
	}
	if _, ok := b.got["input"].([]any); !ok {
		t.Errorf("input = %#v; want a list of items", b.got["input"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := b.got[k]; ok {
			t.Errorf("%s was sent upstream; the backend refuses it", k)
		}
	}
	if b.got["model"] != "gpt-5.5" {
		t.Errorf("model = %v; want gpt-5.5 without the codex/ prefix", b.got["model"])
	}
	if r, ok := b.got["reasoning"].(map[string]any); !ok || r["effort"] != "low" {
		t.Errorf("reasoning parameters not kept: %v", b.got["reasoning"])
	}
	if b.auth != "Bearer "+access {
		t.Errorf("Authorization = %q; want the account token", b.auth)
	}
	if b.acct != "acct-s6" {
		t.Errorf("chatgpt-account-id = %q; want acct-s6", b.acct)
	}
}

const s6Stream = "event: response.output_text.delta\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s6\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"id\":\"msg_s6\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"

// ---- A4 -------------------------------------------------------------------

func TestS6A4NonStreamCodexResponses(t *testing.T) {
	b := &s6Backend{reply: s6Stream}
	access := s6Setup(t, b)

	rec := s6Run(t, `{"model":"codex/gpt-5.5","input":"hello there","max_output_tokens":100,"temperature":0.2,"reasoning":{"effort":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	s6CheckUpstream(t, b, access)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply is not one JSON object: %v: %s", err, rec.Body.String())
	}
	if out["object"] != "response" {
		t.Errorf("object = %v; want response", out["object"])
	}
	if !strings.Contains(rec.Body.String(), "Hello") {
		t.Errorf("reply lacks the generated text: %s", rec.Body.String())
	}
}

// ---- A5 -------------------------------------------------------------------

func TestS6A5StreamCodexResponsesWithoutContentType(t *testing.T) {
	b := &s6Backend{reply: s6Stream, noType: true}
	access := s6Setup(t, b)

	rec := s6Run(t, `{"model":"codex/gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":50,"temperature":0.1,"reasoning":{"effort":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	s6CheckUpstream(t, b, access)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q; want text/event-stream", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"response.output_text.delta", "response.completed", "Hello"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream lacks %q: %s", want, body)
		}
	}
}
