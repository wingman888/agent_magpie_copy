package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// chatgpt stands in for the ChatGPT backend behind CodexBase.
func chatgpt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no sign-ins but the test's
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return up
}

func codexPost(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// One of Codex's own models goes on to the ChatGPT backend as it came, the
// sign-in with it; a summary magpie made earlier goes as the text it holds.
func TestCodexOwnModelPassesThrough(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []byte
	var head http.Header
	var path string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		head, path = r.Header, r.URL.Path
		w.Header()["Content-Type"] = nil // as the ChatGPT backend sends it
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	sum := magpieCompaction + base64.StdEncoding.EncodeToString([]byte("did X, next Y"))
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":[
	  {"type":"compaction","encrypted_content":"`+sum+`"},
	  {"type":"compaction","encrypted_content":"openai-own"},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if code != 200 || !strings.Contains(body, `"input_tokens":9`) {
		t.Fatalf("%d %s", code, body)
	}
	if u := usage.Load(time.Time{}); len(u) != 1 || u[0].Input != 9 || u[0].Output != 2 || u[0].Provider != "openai" {
		t.Errorf("usage %+v", u)
	}
	if path != "/backend-api/codex/responses" || head.Get("Authorization") != "Bearer chatgpt-token" || head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("upstream %s %v", path, head)
	}
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(got, &q)
	if len(q.Input) != 3 || q.Input[0]["type"] != "message" || !strings.Contains(string(got), "did X, next Y") ||
		q.Input[1]["encrypted_content"] != "openai-own" {
		t.Errorf("input: %s", got)
	}
}

// A magpie model is served by magpie, whatever sign-in Codex sent.
func TestCodexMagpieModelServed(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	code, body := codexPost(t, `{"model":"fake/m1","stream":true,"input":"hello"}`)
	if code != 200 || !strings.Contains(body, `"delta":"hi"`) {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(string(f.got), `"model":"m1"`) {
		t.Errorf("upstream got %s", f.got)
	}
}

// Codex compresses what it sends the ChatGPT backend; magpie reads it all
// the same.
func TestCodexReadsCompressedBody(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	enc, _ := zstd.NewWriter(nil)
	z := enc.EncodeAll([]byte(`{"model":"fake/m1","stream":true,"input":"hello"}`), nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(z))
	req.Header.Set("Content-Encoding", "zstd")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delta":"hi"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Compacting a magpie model's conversation: the model summarises, and Codex
// gets the compaction item it asked for, holding the summary.
func TestCodexCompactsMagpieModel(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"SUMM"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"ARY"},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := codexPost(t, `{"model":"fake/m1","stream":true,
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},
	    {"type":"compaction_trigger"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(f.got), `"tools"`) || !strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("upstream got %s", f.got)
	}
	var item map[string]any
	completed := false
	for _, e := range events(body) {
		switch e["type"] {
		case "response.output_item.done":
			item = e["item"].(map[string]any)
		case "response.completed":
			completed = true
		}
	}
	enc, _ := item["encrypted_content"].(string)
	b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, magpieCompaction))
	if item["type"] != "compaction" || !strings.HasPrefix(enc, magpieCompaction) || string(b) != "SUMMARY" || !completed {
		t.Errorf("events: %s", body)
	}
}

// The model list is the backend's for this sign-in, then magpie's.
func TestCodexModelList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var auth string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1}]}`)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models?client_version=0.155.1", nil)
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Models []map[string]any `json:"models"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	var slugs []string
	var fake map[string]any
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		slugs = append(slugs, slug)
		if slug == "fake/m1" {
			fake = m
		}
	}
	// Other sign-ins found on the machine (Cursor's, say) may follow.
	if rec.Code != 200 || len(slugs) < 2 || slugs[0] != "gpt-5.5" || fake == nil || fake["base_instructions"] == "" ||
		rec.Header().Get("ETag") != `"v1"` || auth != "Bearer chatgpt-token" {
		t.Errorf("%d %v %q %q", rec.Code, slugs, rec.Header().Get("ETag"), auth)
	}
	for _, s := range slugs {
		if strings.HasPrefix(s, "codex/") {
			t.Errorf("Codex's own models twice: %v", slugs)
		}
	}
}

// Responses over a WebSocket are turned away so Codex uses HTTP at once.
func TestCodexWebSocketUpgradeRequired(t *testing.T) {
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent to ChatGPT") })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Errorf("%d", rec.Code)
	}
}
