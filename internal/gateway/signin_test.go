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

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/provider"
)

// backend records what it was sent and answers with a fixed reply. An empty
// ctype sends no Content-Type at all, as the ChatGPT stream does.
type backend struct {
	path  string
	head  http.Header
	body  []byte
	reply string
	ctype string
	code  int
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.path, b.head = r.URL.Path, r.Header
	b.body, _ = io.ReadAll(r.Body)
	if b.ctype == "" {
		w.Header()["Content-Type"] = nil // stop net/http sniffing one in
	} else {
		w.Header().Set("Content-Type", b.ctype)
	}
	if b.code != 0 {
		w.WriteHeader(b.code)
	}
	io.WriteString(w, b.reply)
}

func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(p) + ".sig"
}

// sandboxHome points HOME and dial's config at a temp dir.
func sandboxHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return root
}

// signedInCodex writes a ChatGPT login for Codex and points CodexBase at up.
// It returns the access token the login holds.
func signedInCodex(t *testing.T, up *httptest.Server) string {
	t.Helper()
	sandboxHome(t)
	acc := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	auth, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com"}),
			"access_token":  acc,
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
		},
	})
	dir := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	prev := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = prev })
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	return acc
}

func codexStream() string {
	return sse(
		"event: response.created\ndata: "+`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
		"event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","delta":"Hello"}`,
		"event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","delta":" world"}`,
		"event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
	)
}

func TestCodexNonStreamingReturnsWholeResponse(t *testing.T) {
	up := &backend{reply: codexStream()}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	acc := signedInCodex(t, srv)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":10,"temperature":0.5}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("reply is not one JSON object: %v: %s", err, body)
	}
	if out["object"] != "response" || out["status"] != "completed" || !strings.Contains(body, "Hello world") {
		t.Errorf("reply: %s", body)
	}

	if up.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path %s", up.path)
	}
	if got := up.head.Get("Authorization"); got != "Bearer "+acc {
		t.Errorf("authorization %q", got)
	}
	if got := up.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id %q", got)
	}
	var sent map[string]any
	json.Unmarshal(up.body, &sent)
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := sent[k]; ok {
			t.Errorf("upstream got %s: %s", k, up.body)
		}
	}
	if sent["stream"] != true || sent["store"] != false || sent["model"] != "gpt-5.5" {
		t.Errorf("stream/store/model: %s", up.body)
	}
	if _, ok := sent["input"].([]any); !ok {
		t.Errorf("input is not a list: %s", up.body)
	}
}

func TestCodexStreamingIsForwardedWithoutContentType(t *testing.T) {
	up := &backend{reply: codexStream()}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	signedInCodex(t, srv)

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
	  "max_output_tokens":10,"temperature":0.5}`))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("client content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "response.output_text.delta") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	var sent map[string]any
	json.Unmarshal(up.body, &sent)
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("upstream got %s: %s", k, up.body)
		}
	}
	if sent["stream"] != true || sent["store"] != false {
		t.Errorf("stream/store: %s", up.body)
	}
}

func TestCodexStringInputBecomesItemList(t *testing.T) {
	up := &backend{reply: codexStream()}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	signedInCodex(t, srv)

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"just a string"}`))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var sent struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(up.body, &sent); err != nil {
		t.Fatalf("upstream body: %v: %s", err, up.body)
	}
	if len(sent.Input) != 1 || sent.Input[0].Type != "message" || sent.Input[0].Role != "user" ||
		len(sent.Input[0].Content) != 1 || sent.Input[0].Content[0].Text != "just a string" {
		t.Errorf("input: %s", up.body)
	}
}

func TestCodexRejectedLoginSaysLoggedOut(t *testing.T) {
	up := &backend{code: 401, ctype: "application/json", reply: `{"error":"expired"}`}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	signedInCodex(t, srv)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 502 || !strings.Contains(body, "codex login") {
		t.Errorf("%d %s", code, body)
	}
}

// ---- Copilot ----------------------------------------------------------------

func signedInCopilot(t *testing.T, tokenStatus int) *backend {
	t.Helper()
	root := sandboxHome(t)
	dir := filepath.Join(root, "cfg", "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_test"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	chat := &backend{ctype: "text/event-stream", reply: sse(
		`data: {"id":"c1","model":"gpt-x","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	mux := http.NewServeMux()
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenStatus != 0 && tokenStatus != http.StatusOK {
			w.WriteHeader(tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"sess-1","endpoints":{"api":"`+"http://"+r.Host+`/copilot"}}`)
	})
	mux.Handle("/copilot/", chat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = prev })
	if err := provider.Save(provider.Provider{ID: "copilot", Models: []string{"gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	return chat
}

func TestCopilotInitiatorAndVisionHeaders(t *testing.T) {
	cases := []struct {
		name, body, initiator, vision string
	}{
		{"user turn", `{"model":"copilot/gpt-x","messages":[{"role":"user","content":"hi"}]}`, "user", ""},
		{"tool result sent back", `{"model":"copilot/gpt-x","messages":[{"role":"user","content":"hi"},` +
			`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"c1","content":"x"}]}`, "agent", ""},
		{"image in user turn", `{"model":"copilot/gpt-x","messages":[{"role":"user","content":[` +
			`{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, "user", "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chat := signedInCopilot(t, 0)
			code, body := post(t, "/v1/chat/completions", c.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if chat.path != "/copilot/chat/completions" {
				t.Errorf("upstream path %s", chat.path)
			}
			if got := chat.head.Get("Authorization"); got != "Bearer sess-1" {
				t.Errorf("authorization %q", got)
			}
			if got := chat.head.Get("X-Initiator"); got != c.initiator {
				t.Errorf("X-Initiator %q, want %q", got, c.initiator)
			}
			if got := chat.head.Get("Copilot-Vision-Request"); got != c.vision {
				t.Errorf("Copilot-Vision-Request %q, want %q", got, c.vision)
			}
		})
	}
}

func TestCopilotRevokedSessionAsksToSignInAgain(t *testing.T) {
	signedInCopilot(t, http.StatusUnauthorized)
	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 || !strings.Contains(body, "请重新登录 Copilot") {
		t.Errorf("%d %s", code, body)
	}
}
