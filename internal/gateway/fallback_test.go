package gateway

import (
	"bytes"
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

	"github.com/yetone/magpie/internal/provider"
)

// twoProviders: "plan" (the one agents pick) falls back to "spare".
func twoProviders(t *testing.T, plan, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2", "nowhere/x"}}, plan},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, spare},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

const chatReq = `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackWhenOutOfQuota(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"rate limit", 429, "slow down"},
		{"no balance", 402, "Insufficient Balance"},
		{"overloaded", 529, "overloaded"},
		{"quota as 403", 403, "您的套餐额度已用完"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &fake{t: t, ctype: "application/json", code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
			spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
			twoProviders(t, plan, spare)
			code, body := post(t, "/v1/chat/completions", chatReq)
			if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, tc.msg) {
				t.Fatalf("%d %s", code, body)
			}
			if !bytes.Contains(spare.got, []byte(`"model":"m2"`)) {
				t.Fatalf("spare got %s", spare.got)
			}
		})
	}
}

func TestFallbackRestsTheFailedProvider(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Code, rec.Body.String()
	}
	send()
	if c := s.Recent()[0]; c.Provider != "spare" || !strings.Contains(c.Fallback, "plan: ") {
		t.Fatalf("recorded %+v", c)
	}
	plan.got = nil
	if code, body := send(); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.got != nil {
		t.Fatal("a resting provider was tried first")
	}
}

func TestNoFallbackForOtherErrors(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 400 || !strings.Contains(body, "field required") || spare.got != nil {
		t.Fatalf("%d %s (spare got %s)", code, body, spare.got)
	}
}

func TestLastFallbackErrorReachesTheAgent(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"plan limit"}}`}
	spare := &fake{t: t, ctype: "application/json", code: 503, reply: `{"error":{"message":"spare down"}}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 503 || !strings.Contains(body, "spare down") {
		t.Fatalf("%d %s", code, body)
	}
}

// byKey answers per API key: a key named in limited is out of quota.
type byKey struct {
	limited map[string]bool
	seen    []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.seen = append(b.seen, key)
	w.Header().Set("Content-Type", "application/json")
	if b.limited[key] {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func TestSeveralKeysOnTakeOverFromEachOther(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{"k-personal": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-personal", KeyName: "Personal",
		Keys: []provider.KeyAccount{{Name: "Idle", Key: "k-idle", Off: true}, {Name: "Team", Key: "k-team"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Body.String()
	}
	if body := send(); !strings.Contains(body, "from-k-team") {
		t.Fatalf("body %s", body)
	}
	if c := s.Recent()[0]; c.Provider != "plan" || !strings.Contains(c.Fallback, "plan (Personal): ") {
		t.Fatalf("recorded %+v", c)
	}
	// the limited key rests; the one that's off is never tried
	up.seen = nil
	if body := send(); !strings.Contains(body, "from-k-team") || strings.Join(up.seen, ",") != "k-team" {
		t.Fatalf("body %s, tried %v", body, up.seen)
	}
}

// Two ChatGPT accounts ticked: the one Codex is signed in to is out of
// quota, so the request goes to the saved one, signed with its own tokens.
func TestSubscriptionAccountsTakeOver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("me@example.com", "acct-1")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "spare@example.com", "on": true, "seen": time.Now(), "auth": auth("spare@example.com", "acct-2")},
	}), 0o600)

	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"You've hit your usage limit"}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Join(tried, ",") != "acct-1,acct-2" {
		t.Fatalf("tried %v", tried)
	}
	// the first sits out a minute; the next request goes straight on
	tried = nil
	post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("second request tried %v", tried)
	}
}
