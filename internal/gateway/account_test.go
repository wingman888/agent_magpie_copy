package gateway

// Account providers through the gateway. The agent's own login files are
// written into a temporary HOME and XDG_CONFIG_HOME; the ChatGPT and Copilot
// backends are httptest stand-ins pointed at by the provider's variables.

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

// codexSSE is the event stream the ChatGPT backend sends back for one turn.
const codexSSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"world"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}

`

// codexBackend stands in for the ChatGPT backend and records what it got.
// mode picks the reply: "stream" is an event stream with its Content-Type,
// "bare" the same stream with no Content-Type, "refuse" a 401.
type codexBackend struct {
	mode string
	path string
	head http.Header
	body map[string]any
}

func (b *codexBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.path, b.head = r.URL.Path, r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	b.body = nil
	json.Unmarshal(raw, &b.body)
	switch b.mode {
	case "refuse":
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"Could not validate credentials"}`)
	case "bare":
		w.Header()["Content-Type"] = nil // send no type, so the gateway must sniff the body
		io.WriteString(w, codexSSE)
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexSSE)
	}
}

// useCodexBackend points the Codex backend at be for the rest of the test.
func useCodexBackend(t *testing.T, be *codexBackend) {
	t.Helper()
	srv := httptest.NewServer(be)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

// isolateHome gives the test a fresh HOME and dial config and cache.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return home
}

// jwt builds an unsigned token with the given payload; dial only decodes it.
func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// writeCodexLogin writes a ChatGPT-mode Codex login into home and returns its
// access token. The token is far from expiry, so no refresh is attempted.
func writeCodexLogin(t *testing.T, home string) string {
	t.Helper()
	access := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	id := jwt(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": id, "access_token": access, "refresh_token": "rt", "account_id": "acct_123",
		},
	}
	b, _ := json.Marshal(auth)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// gatewayPost sends one request to the gateway's handler.
func gatewayPost(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	return rec
}

// checkCodexRequest verifies what the backend received for a request that
// started as the given body.
func checkCodexRequest(t *testing.T, be *codexBackend, access string) {
	t.Helper()
	if be.path != "/backend-api/codex/responses" {
		t.Errorf("backend path = %q", be.path)
	}
	if be.body["stream"] != true {
		t.Errorf("backend stream = %v, want true", be.body["stream"])
	}
	if be.body["store"] != false {
		t.Errorf("backend store = %v, want false", be.body["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := be.body[k]; ok {
			t.Errorf("backend got %q, which the ChatGPT backend refuses", k)
		}
	}
	if be.body["model"] != "gpt-5.5" {
		t.Errorf("backend model = %v, want bare gpt-5.5", be.body["model"])
	}
	items, ok := be.body["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("backend input = %#v, want a list of one item", be.body["input"])
	}
	if got := be.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := be.head.Get("chatgpt-account-id"); got != "acct_123" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
}

// responseText pulls the text out of a one-message Responses reply.
func responseText(t *testing.T, body []byte) (string, string, int) {
	t.Helper()
	var out struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	if len(out.Output) == 0 || len(out.Output[0].Content) == 0 {
		t.Fatalf("reply has no text: %s", body)
	}
	return out.Object, out.Output[0].Content[0].Text, out.Usage.OutputTokens
}

func TestCodexNonStreamIsFoldedIntoOneResponse(t *testing.T) {
	home := isolateHome(t)
	access := writeCodexLogin(t, home)
	be := &codexBackend{mode: "stream"}
	useCodexBackend(t, be)

	rec := gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	obj, text, outTokens := responseText(t, rec.Body.Bytes())
	if obj != "response" || text != "Hello world" || outTokens != 2 {
		t.Errorf("reply object=%q text=%q output_tokens=%d", obj, text, outTokens)
	}
	checkCodexRequest(t, be, access)
	if first, _ := be.body["input"].([]any); len(first) == 1 {
		item, _ := first[0].(map[string]any)
		if item["type"] != "message" || item["role"] != "user" {
			t.Errorf("input item = %#v, want a user message", item)
		}
	}
}

func TestCodexStreamIsRelayed(t *testing.T) {
	home := isolateHome(t)
	access := writeCodexLogin(t, home)
	be := &codexBackend{mode: "stream"}
	useCodexBackend(t, be)

	rec := gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":16}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want an event stream", ct)
	}
	if got := rec.Body.String(); got != codexSSE {
		t.Errorf("relayed stream differs from the backend's:\n%s", got)
	}
	checkCodexRequest(t, be, access)
}

func TestCodexBackendWithoutContentTypeIsAnEventStream(t *testing.T) {
	home := isolateHome(t)
	writeCodexLogin(t, home)
	be := &codexBackend{mode: "bare"}
	useCodexBackend(t, be)

	rec := gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("stream Content-Type = %q, want text/event-stream", ct)
	}
	if got := rec.Body.String(); got != codexSSE {
		t.Errorf("relayed stream differs from the backend's:\n%s", got)
	}

	rec = gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-stream status %d: %s", rec.Code, rec.Body)
	}
	if _, text, _ := responseText(t, rec.Body.Bytes()); text != "Hello world" {
		t.Errorf("non-stream text = %q", text)
	}
}

func TestCodexRefusedTokenAsksForRelogin(t *testing.T) {
	home := isolateHome(t)
	writeCodexLogin(t, home)
	useCodexBackend(t, &codexBackend{mode: "refuse"})

	for _, body := range []string{
		`{"model":"codex/gpt-5.5","input":"hi"}`,
		`{"model":"codex/gpt-5.5","input":"hi","stream":true}`,
	} {
		rec := gatewayPost(t, "/v1/responses", body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status %d for %s, want 401", rec.Code, body)
		}
		if !strings.Contains(rec.Body.String(), "请重新登录") {
			t.Errorf("error does not ask for a new login: %s", rec.Body)
		}
		if strings.Contains(rec.Body.String(), "Could not validate") {
			t.Errorf("backend text leaked into the error: %s", rec.Body)
		}
	}
}

func TestCodexLoggedOutAsksForRelogin(t *testing.T) {
	isolateHome(t) // no ~/.codex/auth.json: the login is gone
	be := &codexBackend{mode: "stream"}
	useCodexBackend(t, be)

	rec := gatewayPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "请重新登录") {
		t.Errorf("status %d, body %s; want 401 asking for a new login", rec.Code, rec.Body)
	}
	if be.path != "" {
		t.Errorf("backend was called after logout: %s", be.path)
	}
}

func TestCopilotChatIsForwardedWithSessionToken(t *testing.T) {
	isolateHome(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:octo":{"user":"octo","oauth_token":"gho_secret"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotInitiator, gotIntegration, gotModel, gotTokenAuth string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		gotTokenAuth = r.Header.Get("Authorization")
		// A short expiry means every call exchanges again, so the test does not
		// depend on a session cached by an earlier test.
		fmt.Fprintf(w, `{"token":"tid_session","expires_at":%d,"endpoints":{"api":%q}}`,
			time.Now().Unix()+60, srv.URL+"/copilot")
	})
	mux.HandleFunc("/copilot/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		json.Unmarshal(raw, &body)
		gotAuth, gotInitiator = r.Header.Get("Authorization"), r.Header.Get("X-Initiator")
		gotIntegration, gotModel = r.Header.Get("Copilot-Integration-Id"), body.Model
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"claude-sonnet-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	})
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = old })

	rec := gatewayPost(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"ping"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"gho_secret", "tid_session"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("token %q leaked into the reply", secret)
		}
	}
	if gotTokenAuth != "token gho_secret" {
		t.Errorf("token exchange Authorization = %q", gotTokenAuth)
	}
	if gotAuth != "Bearer tid_session" {
		t.Errorf("chat Authorization = %q", gotAuth)
	}
	if gotInitiator != "user" {
		t.Errorf("X-Initiator = %q, want user", gotInitiator)
	}
	if gotIntegration == "" {
		t.Errorf("Copilot-Integration-Id is empty")
	}
	if gotModel != "claude-sonnet-4.5" {
		t.Errorf("backend model = %q, want the bare name", gotModel)
	}
}
