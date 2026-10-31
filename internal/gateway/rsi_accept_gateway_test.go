package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

type upstream struct {
	mu    sync.Mutex
	body  []byte
	head  http.Header
	host  string
	calls int
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.body, u.head, u.host = b, r.Header.Clone(), r.Host
	u.calls++
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","choices":[]}`)
}

func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func send(t *testing.T, path, body string, hdr map[string][]string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header[k] = v
	}
	gateway.New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestAcceptDeveloperRoleBecomesSystem(t *testing.T) {
	isolateEnv(t)
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	in := `{"model":"m1","reasoning_effort":"max","messages":[{"role":"developer","content":"be brief","name":"d1"},{"role":"user","content":"a developer asks"},{"role":"developer","content":"again"}]}`
	if code, out := send(t, "/v1/chat/completions", in, nil); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var got struct {
		Model    string           `json:"model"`
		Effort   string           `json:"reasoning_effort"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(up.body, &got); err != nil {
		t.Fatalf("%v: %s", err, up.body)
	}
	if len(got.Messages) != 3 || got.Effort != "max" {
		t.Fatalf("forwarded: %s", up.body)
	}
	if got.Messages[0]["role"] != "system" || got.Messages[0]["content"] != "be brief" || got.Messages[0]["name"] != "d1" {
		t.Errorf("first: %v", got.Messages[0])
	}
	if got.Messages[1]["role"] != "user" || got.Messages[1]["content"] != "a developer asks" {
		t.Errorf("user message changed: %v", got.Messages[1])
	}
	if got.Messages[2]["role"] != "system" || got.Messages[2]["content"] != "again" {
		t.Errorf("third: %v", got.Messages[2])
	}

	// without developer messages nothing changes
	plain := `{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
	if code, out := send(t, "/v1/chat/completions", plain, nil); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var a, b map[string]any
	json.Unmarshal([]byte(plain), &a)
	if err := json.Unmarshal(up.body, &b); err != nil {
		t.Fatal(err)
	}
	am, _ := json.Marshal(a)
	bm, _ := json.Marshal(b)
	if string(am) != string(bm) {
		t.Errorf("body changed: %s", up.body)
	}
}

// openCode points an OpenCode-hosted provider at the fake by routing the
// gateway through it as a proxy, so the upstream really is opencode.ai.
func openCode(t *testing.T, chat, responses string) *upstream {
	t.Helper()
	isolateEnv(t)
	up := &upstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := settings.Save(settings.Settings{Proxy: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "oc", Name: "OC", Key: "k", Models: []string{"m1"}, Chat: chat, Responses: responses}); err != nil {
		t.Fatal(err)
	}
	return up
}

func TestAcceptOpenCodeSessionHeader(t *testing.T) {
	up := openCode(t, "http://opencode.ai/zen/go/v1", "http://opencode.ai/zen/go/v1")
	chat := func(body string, h map[string][]string) string {
		t.Helper()
		if code, out := send(t, "/v1/chat/completions", body, h); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		if up.host != "opencode.ai" {
			t.Fatalf("upstream host %q", up.host)
		}
		return up.head.Get("x-opencode-session")
	}
	turn1 := `{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"}]}`
	turn2 := `{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"},{"role":"assistant","content":"done"},{"role":"user","content":"thanks"}]}`
	other := `{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"write docs"}]}`
	a, b, c := chat(turn1, nil), chat(turn2, nil), chat(other, nil)
	if !strings.HasPrefix(a, "magpie-") || a != b || a == c || !strings.HasPrefix(c, "magpie-") {
		t.Errorf("derived: %q %q %q", a, b, c)
	}

	// the agent's own session wins, in the documented order
	if got := chat(turn1, map[string][]string{"X-Opencode-Session": {"ses-a"}, "X-Session-Affinity": {"ses-b"}}); got != "ses-a" {
		t.Errorf("opencode header: %q", got)
	}
	if got := chat(turn1, map[string][]string{"X-Session-Affinity": {"ses-b"}, "X-Session-Id": {"ses-c"}, "Session_id": {"ses-d"}}); got != "ses-b" {
		t.Errorf("affinity: %q", got)
	}
	if got := chat(turn1, map[string][]string{"X-Session-Id": {"ses-c"}, "Session_id": {"ses-d"}, "X-Claude-Code-Session-Id": {"ses-e"}}); got != "ses-c" {
		t.Errorf("session id: %q", got)
	}
	if got := chat(turn1, map[string][]string{"Session_id": {"ses-d"}, "Session-Id": {"ses-f"}}); got != "ses-d" {
		t.Errorf("session_id: %q", got)
	}
	if got := chat(turn1, map[string][]string{"X-Claude-Code-Session-Id": {"ses-e"}}); got != "ses-e" {
		t.Errorf("claude code: %q", got)
	}
	if got := chat(turn1, map[string][]string{"X-Opencode-Session": {""}, "X-Session-Id": {"ses-c"}}); got != "ses-c" {
		t.Errorf("empty value should be skipped: %q", got)
	}

	// Responses requests too
	resp := func(body string) string {
		t.Helper()
		if code, out := send(t, "/v1/responses", body, nil); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		return up.head.Get("x-opencode-session")
	}
	r1 := resp(`{"model":"m1","input":[{"role":"user","content":"hi"}]}`)
	r2 := resp(`{"model":"m1","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"x"}]}`)
	r3 := resp(`{"model":"m1","input":[{"role":"user","content":"something else"}]}`)
	if !strings.HasPrefix(r1, "magpie-") || r1 != r2 || r1 == r3 {
		t.Errorf("responses: %q %q %q", r1, r2, r3)
	}
}

func TestAcceptOtherVendorsGetNoSessionHeader(t *testing.T) {
	isolateEnv(t)
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	if code, out := send(t, "/v1/chat/completions", body, map[string][]string{"X-Session-Id": {"abc"}}); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if v := up.head.Get("x-opencode-session"); v != "" {
		t.Errorf("leaked OpenCode header: %q", v)
	}
}

func TestAcceptProviderTestSendsOpenCodeSession(t *testing.T) {
	isolateEnv(t)
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("default transport replaced")
	}
	old := tr.Proxy
	tr.Proxy = func(*http.Request) (*urlT, error) { return parseURL(srv.URL) }
	defer func() { tr.Proxy = old }()
	p := provider.Provider{ID: "oc", Name: "OC", Key: "k", Models: []string{"m1"}, Chat: "http://opencode.ai/zen/go/v1"}
	p.Test(t.Context())
	if up.calls == 0 {
		t.Skip("provider test did not use the default transport")
	}
	if up.head.Get("x-opencode-session") == "" {
		t.Errorf("no session header on provider test: %v", up.head)
	}
}

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

func TestAcceptLoopbackUpstreamBypassesProxy(t *testing.T) {
	isolateEnv(t)
	up := &upstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:1"}); err != nil { // nothing listens there
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	if code, out := send(t, "/v1/chat/completions", body, nil); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
}
