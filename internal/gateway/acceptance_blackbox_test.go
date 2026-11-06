package gateway_test

// Black-box checks of the gateway's HTTP behaviour for account providers
// (acceptance 3–7 and the Copilot header rules). Requests go through
// gateway.New().Handler(); upstreams are httptest stand-ins for the ChatGPT
// Codex backend, the Codex token endpoint and GitHub's Copilot API.

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
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

func bbHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func bbWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func bbJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func bbSetString(t *testing.T, p *string, v string) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// bbCodexLogin writes a ChatGPT-mode Codex sign-in with fields dial must keep
// when it renews the token. It returns the access token.
func bbCodexLogin(t *testing.T, home string, exp int64) string {
	t.Helper()
	id := bbJWT(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	access := bbJWT(map[string]any{"exp": exp})
	bbWrite(t, filepath.Join(home, ".codex", "auth.json"),
		fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"custom":{"keep":true},"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"R1","account_id":"acct-1","extra":"kept"}}`, id, access))
	return access
}

// bbStream is what the Codex backend sends: an event stream with no Content-Type.
const bbStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}

`

// bbCodex stands in for the ChatGPT Codex backend.
type bbCodex struct {
	mu   sync.Mutex
	got  map[string]any
	auth string
	acct string
	path string
	hits int
}

func (b *bbCodex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m := map[string]any{}
	json.Unmarshal(raw, &m)
	b.mu.Lock()
	b.hits++
	b.got, b.auth, b.acct, b.path = m, r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id"), r.URL.Path
	b.mu.Unlock()
	w.Header()["Content-Type"] = nil
	io.WriteString(w, bbStream)
}

// bbCodexUp starts a Codex stand-in and points provider.CodexBase at it.
func bbCodexUp(t *testing.T) *bbCodex {
	t.Helper()
	b := &bbCodex{}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	bbSetString(t, &provider.CodexBase, up.URL)
	return b
}

// bbCheckBackend verifies what the Codex backend received for a codex/gpt-5.5
// Responses request sent with input "hi there", max_output_tokens, temperature
// and reasoning effort low.
func bbCheckBackend(t *testing.T, b *bbCodex, access string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	got := b.got
	if got["stream"] != true {
		t.Errorf("backend stream = %v, want true", got["stream"])
	}
	if got["store"] != false {
		t.Errorf("backend store = %v, want false", got["store"])
	}
	in, ok := got["input"].([]any)
	if !ok || len(in) == 0 {
		t.Fatalf("backend input is not a list: %v", got["input"])
	}
	first, _ := in[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("first input item role = %v, want user", first["role"])
	}
	if raw, _ := json.Marshal(in[0]); !strings.Contains(string(raw), "hi there") {
		t.Errorf("string input not carried into the list: %s", raw)
	}
	for _, k := range []string{"max_output_tokens", "max_tokens", "temperature", "top_p"} {
		if _, present := got[k]; present {
			t.Errorf("backend got forbidden %s", k)
		}
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("backend model = %v, want gpt-5.5 without the provider prefix", got["model"])
	}
	if _, ok := got["reasoning"]; !ok {
		t.Errorf("reasoning was dropped: %v", got)
	}
	if b.auth != "Bearer "+access {
		t.Errorf("backend Authorization = %q", b.auth)
	}
	if b.acct != "acct-1" {
		t.Errorf("backend chatgpt-account-id = %q, want acct-1", b.acct)
	}
	if b.path != "/responses" {
		t.Errorf("backend path = %q, want /responses", b.path)
	}
}

// ---- acceptance 4: non-streaming request answered with one response object ----

func TestAcceptance4NonStreamingCodexResponses(t *testing.T) {
	access := bbCodexLogin(t, bbHome(t), time.Now().Add(time.Hour).Unix())
	b := bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi there","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, rec.Body.String())
	}
	if res.Object != "response" {
		t.Errorf("object = %q, want response", res.Object)
	}
	var text string
	for _, o := range res.Output {
		for _, c := range o.Content {
			text += c.Text
		}
	}
	if !strings.Contains(text, "Hello") {
		t.Errorf("reply text = %q, want the generated text", text)
	}
	bbCheckBackend(t, b, access)
}

// ---- acceptance 5: streaming relayed; no Content-Type from the backend ----

func TestAcceptance5StreamingCodexRelayedWithoutBackendContentType(t *testing.T) {
	access := bbCodexLogin(t, bbHome(t), time.Now().Add(time.Hour).Unix())
	b := bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi there","stream":true,"max_output_tokens":8,"reasoning":{"effort":"low"}}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if strings.Count(body, `"type":"response.output_text.delta"`) != 2 || !strings.Contains(body, "response.completed") {
		t.Errorf("stream not relayed in full: %s", body)
	}
	bbCheckBackend(t, b, access)
}

// A chat client reaches the same backend through translation; the reply must
// still be built, which needs the backend's bodyless-Content-Type stream to be
// recognised as an event stream.
func TestAcceptance5EventStreamWithoutContentTypeIsRecognised(t *testing.T) {
	bbCodexLogin(t, bbHome(t), time.Now().Add(time.Hour).Unix())
	bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi there"}]}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hello") {
		t.Errorf("reply lacks the streamed text: %s", rec.Body.String())
	}
}

// ---- acceptance 6: token renewed before the request; failure asks to sign in ----

func TestAcceptance6ExpiringTokenRenewedAndWrittenBack(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(2*time.Minute).Unix())
	newTok := bbJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})

	var mu sync.Mutex
	refreshes := 0
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		refreshes++
		mu.Unlock()
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "R1" {
			http.Error(w, "bad refresh request", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"R2"}`, newTok)
	}))
	t.Cleanup(ref.Close)
	bbSetString(t, &provider.CodexRefreshURL, ref.URL)
	b := bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi there"}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if refreshes != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshes)
	}
	mu.Unlock()
	b.mu.Lock()
	if b.auth != "Bearer "+newTok {
		t.Errorf("backend got Authorization %q, want the renewed token", b.auth)
	}
	b.mu.Unlock()

	path := filepath.Join(home, ".codex", "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	tokens, ok := f["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing after write-back: %s", raw)
	}
	if tokens["access_token"] != newTok || tokens["refresh_token"] != "R2" {
		t.Errorf("renewed tokens not written back: %v", tokens)
	}
	if tokens["extra"] != "kept" || tokens["account_id"] != "acct-1" || tokens["id_token"] == nil {
		t.Errorf("token fields lost on write-back: %v", tokens)
	}
	if _, ok := f["custom"]; !ok {
		t.Errorf("top-level field lost on write-back: %s", raw)
	}
	if _, ok := f["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY field lost on write-back: %s", raw)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestAcceptance6RefreshFailureAsksToSignInAgain(t *testing.T) {
	bbCodexLogin(t, bbHome(t), time.Now().Add(time.Minute).Unix())
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(ref.Close)
	bbSetString(t, &provider.CodexRefreshURL, ref.URL)
	b := bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi there"}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Errorf("message does not ask to sign in again: %s", rec.Body.String())
	}
	b.mu.Lock()
	if b.hits != 0 {
		t.Errorf("backend was called despite a failed renewal")
	}
	b.mu.Unlock()
}

// ---- acceptance 3 at the gateway: logged out with a leftover selection ----

func TestAcceptance3LoggedOutCodexAnswers401WithSelectionLeft(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(time.Hour).Unix())
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	b := bbCodexUp(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi there"}`))
	gateway.New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "signed out") {
		t.Errorf("message does not say the sign-in is gone: %s", rec.Body.String())
	}
	b.mu.Lock()
	if b.hits != 0 {
		t.Errorf("backend called without a sign-in")
	}
	b.mu.Unlock()
}

// ---- acceptance 7 at the gateway: Copilot chat with initiator markers ----

type bbCopilotAPI struct {
	mu        sync.Mutex
	chatHead  http.Header
	chatHits  int
}

func (c *bbCopilotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		if r.Header.Get("Authorization") != "token gho_TEST" {
			http.Error(w, "bad oauth", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"SESSION-TOK","expires_at":%d,"endpoints":{"api":%q}}`,
			time.Now().Add(time.Hour).Unix(), "http://"+r.Host+"/api")
	case "/api/chat/completions":
		c.chatHits++
		c.chatHead = r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	default:
		http.NotFound(w, r)
	}
}

func TestAcceptance7CopilotChatThroughGatewayCarriesInitiator(t *testing.T) {
	home := bbHome(t)
	bbWrite(t, filepath.Join(home, "config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octocat","oauth_token":"gho_TEST"}}`)
	c := &bbCopilotAPI{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	bbSetString(t, &provider.CopilotTokenURL, srv.URL+"/copilot_internal/v2/token")

	send := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		gateway.New().Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := send(`{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"tool","tool_call_id":"t1","content":"42"}]}`)
	if code != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("tool-last: status %d: %s", code, body)
	}
	c.mu.Lock()
	if got := c.chatHead.Get("X-Initiator"); got != "agent" {
		t.Errorf("tool-last X-Initiator = %q, want agent", got)
	}
	if got := c.chatHead.Get("Authorization"); got != "Bearer SESSION-TOK" {
		t.Errorf("Authorization = %q, want the session token", got)
	}
	c.mu.Unlock()

	code, body = send(`{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("user-last: status %d: %s", code, body)
	}
	c.mu.Lock()
	if got := c.chatHead.Get("X-Initiator"); got != "user" {
		t.Errorf("user-last X-Initiator = %q, want user", got)
	}
	c.mu.Unlock()
}

// The catalog endpoint lists account models under their provider ids.
func TestAcceptance1ModelsEndpointListsAccountModels(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(time.Hour).Unix())
	bbWrite(t, filepath.Join(home, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.test":{"user":"octocat","oauth_token":"gho_TEST"}}`)

	rec := httptest.NewRecorder()
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"codex/gpt-5.5"`, `"copilot/claude-sonnet-4.5"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/v1/models lacks %s", want)
		}
	}
	if strings.Contains(body, `"copilot/auto"`) {
		t.Errorf("/v1/models offers copilot/auto before a refresh")
	}
}
