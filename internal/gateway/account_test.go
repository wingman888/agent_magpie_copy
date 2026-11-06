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
	"time"

	"github.com/yetone/dial/internal/provider"
)

// codexBackend stands in for the ChatGPT Codex backend. It records the request
// it got and answers with an event stream and no Content-Type, as the real
// backend does.
type codexBackend struct {
	got  map[string]any
	head http.Header
	path string
	hits int
}

func (c *codexBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.hits++
	c.head, c.path = r.Header, r.URL.Path
	c.got = map[string]any{}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &c.got)
	w.Header()["Content-Type"] = nil
	io.WriteString(w, sse(
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hel"}`,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
	))
}

// codexJWT is an unsigned token whose payload carries the given exp.
func codexJWT(exp int64, extra string) string {
	payload := fmt.Sprintf(`{"exp":%d%s}`, exp, extra)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

// codexHome points HOME at a temp tree and writes a ChatGPT sign-in whose
// access token expires at exp.
func codexHome(t *testing.T, exp int64) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	access := codexJWT(exp, "")
	id := codexJWT(time.Now().Add(time.Hour).Unix(), `,"email":"dev@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}`)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"custom":{"keep":true},"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"R1","account_id":"acct-1","extra":"kept"}}`, id, access)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func withCodexBackend(t *testing.T, c *codexBackend) {
	t.Helper()
	up := httptest.NewServer(c)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
}

func checkCodexBackendBody(t *testing.T, got map[string]any, stream bool) {
	t.Helper()
	if got["stream"] != true {
		t.Errorf("backend got stream=%v, want true", got["stream"])
	}
	if got["store"] != false {
		t.Errorf("backend got store=%v, want false", got["store"])
	}
	in, ok := got["input"].([]any)
	if !ok || len(in) != 1 {
		t.Fatalf("backend input is not a one-item list: %v", got["input"])
	}
	item := in[0].(map[string]any)
	if item["role"] != "user" {
		t.Errorf("input item role = %v", item["role"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := got[k]; ok {
			t.Errorf("backend got forbidden %s", k)
		}
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("backend model = %v, want gpt-5.5", got["model"])
	}
	if _, ok := got["reasoning"]; !ok {
		t.Errorf("reasoning was dropped: %v", got)
	}
}

func TestCodexNonStreamingResponses(t *testing.T) {
	codexHome(t, time.Now().Add(time.Hour).Unix())
	c := &codexBackend{}
	withCodexBackend(t, c)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var res struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	if res.Object != "response" || len(res.Output) == 0 || res.Output[0].Content[0].Text != "Hello" {
		t.Fatalf("bad reply: %s", body)
	}
	if res.Usage.OutputTokens != 2 {
		t.Errorf("usage not carried: %+v", res.Usage)
	}
	checkCodexBackendBody(t, c.got, false)
	if c.path != "/responses" {
		t.Errorf("backend path = %q", c.path)
	}
	if c.head.Get("Authorization") == "" || c.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("missing account headers: %v", c.head)
	}
}

func TestCodexStreamingResponsesRelayed(t *testing.T) {
	codexHome(t, time.Now().Add(time.Hour).Unix())
	c := &codexBackend{}
	withCodexBackend(t, c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":8}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type = %q, want an event stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "response.output_text.delta") || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	checkCodexBackendBody(t, c.got, true)
}

func TestCodexTokenRefreshedAndWrittenBack(t *testing.T) {
	newAccess := codexJWT(time.Now().Add(time.Hour).Unix(), "")
	refreshed := 0
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshed++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["refresh_token"] != "R1" || in["grant_type"] != "refresh_token" {
			http.Error(w, "bad refresh", 400)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"R2"}`, newAccess)
	}))
	defer ref.Close()
	old := provider.CodexRefreshURL
	provider.CodexRefreshURL = ref.URL
	t.Cleanup(func() { provider.CodexRefreshURL = old })

	home := codexHome(t, time.Now().Add(2*time.Minute).Unix())
	c := &codexBackend{}
	withCodexBackend(t, c)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if refreshed != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshed)
	}
	if got := c.head.Get("Authorization"); got != "Bearer "+newAccess {
		t.Errorf("backend got old token: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	tok := f["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "R2" {
		t.Errorf("tokens not written back: %v", tok)
	}
	if tok["extra"] != "kept" || f["custom"] == nil || tok["account_id"] != "acct-1" || f["last_refresh"] == nil {
		t.Errorf("other fields lost: %s", raw)
	}
	st, err := os.Stat(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestCodexRefreshFailureAsksForSignIn(t *testing.T) {
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, 401)
	}))
	defer ref.Close()
	old := provider.CodexRefreshURL
	provider.CodexRefreshURL = ref.URL
	t.Cleanup(func() { provider.CodexRefreshURL = old })

	codexHome(t, time.Now().Add(time.Minute).Unix())
	c := &codexBackend{}
	withCodexBackend(t, c)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 {
		t.Fatalf("status %d, want 401: %s", code, body)
	}
	if !strings.Contains(body, "codex login") || !strings.Contains(body, "expired") {
		t.Errorf("message does not ask to sign in again: %s", body)
	}
	if c.hits != 0 {
		t.Errorf("backend was called despite a failed refresh")
	}
}

func TestCodexSignedOutIsExplicit(t *testing.T) {
	home := codexHome(t, time.Now().Add(time.Hour).Unix())
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	c := &codexBackend{}
	withCodexBackend(t, c)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 {
		t.Fatalf("status %d, want 401: %s", code, body)
	}
	if !strings.Contains(body, "signed out") || !strings.Contains(body, "codex login") {
		t.Errorf("message does not say the sign-in is gone: %s", body)
	}
	if c.hits != 0 {
		t.Errorf("backend was called without a sign-in")
	}
}

func TestEventStreamWithoutContentTypeIsDetected(t *testing.T) {
	codexHome(t, time.Now().Add(time.Hour).Unix())
	c := &codexBackend{}
	withCodexBackend(t, c)

	// non-streaming client: the reply is built from the stream, which the
	// backend sent with no Content-Type
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 || !strings.Contains(body, "Hello") {
		t.Fatalf("status %d: %s", code, body)
	}
}
