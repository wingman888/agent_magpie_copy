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

// codexUpT is a fake Codex backend. It records what it got and replies with
// a script; noType drops the Content-Type header, as the real backend may.
type codexUpT struct {
	head   http.Header
	body   []byte
	path   string
	reply  string
	noType bool
}

func (f *codexUpT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.body, _ = io.ReadAll(r.Body)
	f.head, f.path = r.Header, r.URL.Path
	if f.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, f.reply)
}

func codexUpTJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

// codexUpTSetup signs Codex in (a fresh HOME with an auth.json whose access
// token is valid for two hours) and points the Codex backend at the fake. It
// returns the access token the fake should be sent.
func codexUpTSetup(t *testing.T, f *codexUpT) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	exp := time.Now().Add(2 * time.Hour).Unix()
	idTok := codexUpTJWT(t, map[string]any{"email": "dev@example.com"})
	accTok := codexUpTJWT(t, map[string]any{"exp": exp})
	auth := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"` + idTok +
		`","access_token":"` + accTok + `","refresh_token":"rt-1","account_id":"acct-42"},"last_refresh":"x"}`
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
	return accTok
}

// codexUpTPost sends a request to the gateway and returns the recorder.
func codexUpTPost(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec
}

const codexUpTDone = `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`
const codexUpTText = `data: {"type":"response.output_text.delta","delta":"hel"}`
const codexUpTItem = `data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`
const codexUpTCompleted = `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}`

func TestCodexUpTHeadersAndBody(t *testing.T) {
	f := &codexUpT{reply: sse(codexUpTDone, codexUpTText, codexUpTItem, codexUpTCompleted)}
	access := codexUpTSetup(t, f)
	rec := codexUpTPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":50,"temperature":0.2,"top_p":0.9}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization %q", got)
	}
	if got := f.head.Get("chatgpt-account-id"); got != "acct-42" {
		t.Errorf("chatgpt-account-id %q", got)
	}
	if f.path != "/responses" {
		t.Errorf("path %s", f.path)
	}
	var got map[string]any
	if err := json.Unmarshal(f.body, &got); err != nil {
		t.Fatalf("upstream body %s: %v", f.body, err)
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("model %v", got["model"])
	}
	if got["stream"] != true || got["store"] != false {
		t.Errorf("stream/store: %s", f.body)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must not reach Codex: %s", k, f.body)
		}
	}
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input should be a list of one item: %s", f.body)
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["type"] != "input_text" || content["text"] != "hi" {
		t.Errorf("input item: %s", f.body)
	}
}

func TestCodexUpTNonStreamAggregates(t *testing.T) {
	f := &codexUpT{reply: sse(codexUpTDone, codexUpTText, codexUpTItem, codexUpTCompleted)}
	codexUpTSetup(t, f)
	rec := codexUpTPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not one JSON response: %s", rec.Body.String())
	}
	if out.Status != "completed" || len(out.Output) != 1 {
		t.Fatalf("aggregated reply: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello"`) {
		t.Errorf("output text missing: %s", rec.Body.String())
	}
	if out.Usage.InputTokens != 7 {
		t.Errorf("usage: %s", rec.Body.String())
	}
	if !strings.Contains(string(f.body), `"stream":true`) {
		t.Errorf("upstream was not streamed: %s", f.body)
	}
}

func TestCodexUpTStreamPassthrough(t *testing.T) {
	f := &codexUpT{reply: sse(codexUpTDone, codexUpTText, codexUpTItem, codexUpTCompleted)}
	codexUpTSetup(t, f)
	rec := codexUpTPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":9,"temperature":1}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("content type %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"delta":"hel"`) || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	if bodyHas(f.body, "max_output_tokens") || bodyHas(f.body, "temperature") {
		t.Errorf("sampling or output fields reached Codex: %s", f.body)
	}
	if !bodyHas(f.body, `"stream":true`) || !bodyHas(f.body, `"store":false`) {
		t.Errorf("stream/store: %s", f.body)
	}
}

func TestCodexUpTNoContentTypeIsSniffed(t *testing.T) {
	f := &codexUpT{noType: true, reply: sse(codexUpTDone, codexUpTText, codexUpTItem, codexUpTCompleted)}
	codexUpTSetup(t, f)
	rec := codexUpTPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"hello"`) {
		t.Fatalf("event stream without Content-Type not read: %d %s", rec.Code, rec.Body.String())
	}
	f.reply = sse(codexUpTDone, codexUpTText)
	rec = codexUpTPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delta":"hel"`) {
		t.Fatalf("stream without Content-Type not relayed: %d %s", rec.Code, rec.Body.String())
	}
}

func bodyHas(b []byte, s string) bool { return strings.Contains(string(b), s) }
