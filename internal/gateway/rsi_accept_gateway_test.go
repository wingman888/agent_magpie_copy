package gateway_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

var origEnv = os.Environ()

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func magpieBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "magpie-accept-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "magpie")
		cmd := exec.Command("go", "build", "-tags", "nogui", "-o", binPath, "github.com/yetone/magpie")
		cmd.Env = append(append([]string{}, origEnv...), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func putProviders(t *testing.T, ps ...map[string]any) {
	t.Helper()
	writeJSON(t, provider.Path(), map[string]any{"providers": ps})
}

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// up is a scripted upstream.
type up struct {
	mu    sync.Mutex
	code  int
	ctype string
	reply string
	got   [][]byte
	paths []string
}

func (u *up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got = append(u.got, b)
	u.paths = append(u.paths, r.URL.Path)
	u.mu.Unlock()
	ct := u.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.reply)
}

func (u *up) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.got)
}

func (u *up) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.got) == 0 {
		return ""
	}
	return string(u.got[len(u.got)-1])
}

func serve(t *testing.T, h http.Handler) string {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func send(s *gateway.Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const chatOK = `{"id":"from-%s","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

func chatReq(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
}

func TestAcceptCustomHeadersReachTheUpstream(t *testing.T) {
	isolate(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var heads []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				var head strings.Builder
				n := 0
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if l := strings.ToLower(line); strings.HasPrefix(l, "content-length:") {
						n, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
					}
					head.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				io.CopyN(io.Discard, br, int64(n))
				mu.Lock()
				heads = append(heads, head.String())
				mu.Unlock()
				body := fmt.Sprintf(chatOK, "raw")
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}()
		}
	}()
	base := "http://" + ln.Addr().String() + "/v1"
	putProviders(t,
		map[string]any{"id": "hdr", "name": "Hdr", "key": "k-secret", "chat": base, "models": []string{"m1"},
			"headers": map[string]any{"x-Org-ID": "acme", "Authorization": "Bearer override"}},
		map[string]any{"id": "plain", "name": "Plain", "key": "k-secret", "chat": base, "models": []string{"m1"}},
		map[string]any{"id": "deepseek", "name": "DeepSeek", "preset": "deepseek", "key": "k-secret", "chat": base, "models": []string{"m1"},
			"headers": map[string]any{"x-Org-ID": "acme"}},
	)
	s := gateway.New()
	for _, id := range []string{"hdr", "plain", "deepseek"} {
		if code, body := send(s, "/v1/chat/completions", chatReq(id+"/m1")); code != 200 {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heads) != 3 {
		t.Fatalf("%d upstream requests", len(heads))
	}
	if !strings.Contains(heads[0], "\r\nx-Org-ID: acme\r\n") {
		t.Errorf("custom header missing or re-cased:\n%s", heads[0])
	}
	if !strings.Contains(heads[0], "\r\nAuthorization: Bearer override\r\n") || strings.Contains(heads[0], "k-secret") && strings.Contains(heads[0], "Authorization: Bearer k-secret") {
		t.Errorf("custom header did not override the auth header:\n%s", heads[0])
	}
	for _, h := range heads[1:] {
		if strings.Contains(strings.ToLower(h), "x-org-id") {
			t.Errorf("header sent to a provider that has none:\n%s", h)
		}
	}
}

func TestAcceptFallbackOnQuotaErrors(t *testing.T) {
	for i, tc := range []struct {
		code int
		msg  string
	}{
		{429, "slow down"},
		{402, "Insufficient Balance"},
		{529, "overloaded"},
		{403, "您的套餐额度已用完"},
	} {
		plan := &up{code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
		spare := &up{reply: fmt.Sprintf(chatOK, "spare")}
		isolate(t)
		id := fmt.Sprintf("plan%d", i)
		sp := fmt.Sprintf("spare%d", i)
		putProviders(t,
			map[string]any{"id": id, "name": "Plan", "key": "k", "chat": serve(t, plan) + "/v1", "models": []string{"m1"},
				"fallback": []string{"nowhere/x", sp + "/m2"}},
			map[string]any{"id": sp, "name": "Spare", "key": "k", "chat": serve(t, spare) + "/v1", "models": []string{"m2"}},
		)
		s := gateway.New()
		code, body := send(s, "/v1/chat/completions", chatReq(id+"/m1"))
		if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, tc.msg) {
			t.Fatalf("%d: %d %s", tc.code, code, body)
		}
		if !strings.Contains(spare.last(), `"model":"m2"`) {
			t.Fatalf("%d: fallback got %s", tc.code, spare.last())
		}
		rec := s.Recent()
		if len(rec) == 0 || rec[0].Provider != sp {
			t.Fatalf("%d: recorded %+v", tc.code, rec)
		}
		if j, _ := json.Marshal(rec[0]); !strings.Contains(string(j), id) || rec[0].Fallback == "" {
			t.Fatalf("%d: the fallback isn't visible in the call record: %s", tc.code, j)
		}
	}
}

func TestAcceptFallbackRestsTheFailedProvider(t *testing.T) {
	isolate(t)
	plan := &up{code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &up{reply: fmt.Sprintf(chatOK, "spare")}
	putProviders(t,
		map[string]any{"id": "restplan", "name": "Plan", "key": "k", "chat": serve(t, plan) + "/v1", "models": []string{"m1"}, "fallback": []string{"restspare/m2"}},
		map[string]any{"id": "restspare", "name": "Spare", "key": "k", "chat": serve(t, spare) + "/v1", "models": []string{"m2"}},
	)
	s := gateway.New()
	for i := 0; i < 2; i++ {
		if code, body := send(s, "/v1/chat/completions", chatReq("restplan/m1")); code != 200 || !strings.Contains(body, "from-spare") {
			t.Fatalf("request %d: %d %s", i, code, body)
		}
	}
	if plan.hits() != 1 {
		t.Fatalf("the failed provider was tried %d times; it should rest", plan.hits())
	}
	if spare.hits() != 2 {
		t.Fatalf("spare served %d", spare.hits())
	}
}

func TestAcceptNoFallbackForOtherErrorsAndLastErrorSurfaces(t *testing.T) {
	isolate(t)
	bad := &up{code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	spare := &up{reply: fmt.Sprintf(chatOK, "spare")}
	putProviders(t,
		map[string]any{"id": "badplan", "name": "Plan", "key": "k", "chat": serve(t, bad) + "/v1", "models": []string{"m1"}, "fallback": []string{"badspare/m2"}},
		map[string]any{"id": "badspare", "name": "Spare", "key": "k", "chat": serve(t, spare) + "/v1", "models": []string{"m2"}},
	)
	code, body := send(gateway.New(), "/v1/chat/completions", chatReq("badplan/m1"))
	if code != 400 || !strings.Contains(body, "field required") || spare.hits() != 0 {
		t.Fatalf("%d %s (spare hits %d)", code, body, spare.hits())
	}

	plan := &up{code: 429, reply: `{"error":{"message":"plan limit"}}`}
	down := &up{code: 503, reply: `{"error":{"message":"spare down"}}`}
	putProviders(t,
		map[string]any{"id": "lastplan", "name": "Plan", "key": "k", "chat": serve(t, plan) + "/v1", "models": []string{"m1"}, "fallback": []string{"lastspare/m2"}},
		map[string]any{"id": "lastspare", "name": "Spare", "key": "k", "chat": serve(t, down) + "/v1", "models": []string{"m2"}},
	)
	code, body = send(gateway.New(), "/v1/chat/completions", chatReq("lastplan/m1"))
	if code != 503 || !strings.Contains(body, "spare down") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAcceptFallbackForStreamingRequests(t *testing.T) {
	isolate(t)
	plan := &up{code: 529, reply: `{"error":{"message":"overloaded"}}`}
	spare := &up{ctype: "text/event-stream", reply: sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from-spare"}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`)}
	putProviders(t,
		map[string]any{"id": "strplan", "name": "Plan", "key": "k", "chat": serve(t, plan) + "/v1", "models": []string{"m1"}, "fallback": []string{"strspare/m2"}},
		map[string]any{"id": "strspare", "name": "Spare", "key": "k", "chat": serve(t, spare) + "/v1", "models": []string{"m2"}},
	)
	code, body := send(gateway.New(), "/v1/chat/completions",
		`{"model":"strplan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, "overloaded") {
		t.Fatalf("%d %s", code, body)
	}
}

// byKey answers per API key: keys in limited are out of quota.
type byKey struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.seen = append(b.seen, key)
	lim := b.limited[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if lim {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, fmt.Sprintf(chatOK, key))
}

func TestAcceptSeveralKeysTakeOverFromEachOther(t *testing.T) {
	isolate(t)
	k := &byKey{limited: map[string]bool{"k-personal": true}}
	spare := &up{reply: fmt.Sprintf(chatOK, "model-fallback")}
	putProviders(t,
		map[string]any{"id": "multikey", "name": "Plan", "chat": serve(t, k) + "/v1", "models": []string{"m1"},
			"key": "k-personal", "keyName": "Personal",
			"keys":     []any{map[string]any{"name": "Idle", "key": "k-idle", "off": true}, map[string]any{"name": "Team", "key": "k-team"}},
			"fallback": []string{"keyspare/m2"}},
		map[string]any{"id": "keyspare", "name": "Spare", "key": "k", "chat": serve(t, spare) + "/v1", "models": []string{"m2"}},
	)
	s := gateway.New()
	if code, body := send(s, "/v1/chat/completions", chatReq("multikey/m1")); code != 200 || !strings.Contains(body, "from-k-team") {
		t.Fatalf("%d %s", code, body)
	}
	rec := s.Recent()
	if j, _ := json.Marshal(rec[0]); rec[0].Provider != "multikey" || !strings.Contains(string(j), "Personal") {
		t.Fatalf("recorded %s", j)
	}
	// the limited key rests; the one that's off is never tried
	k.mu.Lock()
	k.seen = nil
	k.mu.Unlock()
	if code, body := send(s, "/v1/chat/completions", chatReq("multikey/m1")); code != 200 || !strings.Contains(body, "from-k-team") {
		t.Fatalf("%d %s", code, body)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if strings.Join(k.seen, ",") != "k-team" {
		t.Fatalf("second request tried %v", k.seen)
	}
	if spare.hits() != 0 {
		t.Fatal("the fallback model was used while another key was still available")
	}
}

// A relay with one key for Anthropic and one for OpenAI.
func protocolRelay(t *testing.T, keys [2]map[string]any, models []string) (*gateway.Server, *[]string, *sync.Mutex) {
	isolate(t)
	var mu sync.Mutex
	var seen []string
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+key)
		mu.Unlock()
		if (r.URL.Path == "/v1/messages") != (key == "k-ant") {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"this key is for another protocol"}}`)
			return
		}
		stream := bytes.Contains(b, []byte(`"stream":true`))
		if r.URL.Path == "/v1/messages" {
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse(
					`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
					`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
					`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
					`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
					`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fmt.Sprintf(chatOK, "oai"))
	}))
	p := map[string]any{"id": "relay", "name": "Relay", "chat": srv + "/v1", "anthropic": srv, "models": models}
	for k, v := range keys[0] {
		p[k] = v
	}
	p["keys"] = []any{keys[1]}
	putProviders(t, p)
	return gateway.New(), &seen, &mu
}

func TestAcceptKeysMadeForOneProtocol(t *testing.T) {
	ant := map[string]any{"key": "k-ant", "protocol": "anthropic"}
	oai := map[string]any{"key": "k-oai", "protocol": "chat"}
	orders := map[string][2]map[string]any{
		"openai first": {{"key": "k-oai", "keyProtocol": "chat"}, ant},
		"claude first": {{"key": "k-ant", "keyProtocol": "anthropic"}, oai},
	}
	for name, keys := range orders {
		t.Run(name, func(t *testing.T) {
			s, seen, mu := protocolRelay(t, keys, []string{"claude-opus-4-8", "gpt-5.5"})
			for _, x := range []struct{ path, body, want string }{
				// Claude Code asking for Claude: the Anthropic key, relayed as-is
				{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
				{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
				// an OpenAI agent asking for Claude: translated, to the Anthropic key
				{"/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
				// Claude Code asking for GPT: translated, to the OpenAI key
				{"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
			} {
				mu.Lock()
				*seen = nil
				mu.Unlock()
				code, body := send(s, x.path, x.body)
				mu.Lock()
				got := append([]string{}, *seen...)
				mu.Unlock()
				if code != 200 || len(got) != 1 || got[0] != x.want {
					t.Fatalf("%s %.60s want %s: %d %s, upstream saw %v", x.path, strings.ReplaceAll(x.body, `"`, "'"), x.want, code, strings.NewReplacer(`"`, "'").Replace(body), got)
				}
			}
		})
	}
}

func TestAcceptKeyForAnEndpointTheProviderLacksIsSkipped(t *testing.T) {
	isolate(t)
	k := &byKey{}
	putProviders(t, map[string]any{"id": "chatonly", "name": "ChatOnly", "chat": serve(t, k) + "/v1", "models": []string{"gpt-5.5"},
		"key": "k-ant", "keyProtocol": "anthropic",
		"keys": []any{map[string]any{"key": "k-oai", "protocol": "chat"}}})
	code, body := send(gateway.New(), "/v1/chat/completions", chatReq("chatonly/gpt-5.5"))
	k.mu.Lock()
	defer k.mu.Unlock()
	if code != 200 || strings.Join(k.seen, ",") != "k-oai" {
		t.Fatalf("%d %s, keys tried %v", code, body, k.seen)
	}
}

// Two ChatGPT accounts in use: the one Codex is signed in to is out of
// quota, so the request goes to the saved one, signed with its own tokens.
func TestAcceptSubscriptionAccountsTakeOver(t *testing.T) {
	home := isolate(t)
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	login := func(email, acct string) {
		writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}})
	}
	remember := func() {
		cmd := exec.Command(magpieBin(t), "accounts")
		cmd.Env = append(append([]string{}, origEnv...), "HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("magpie accounts: %v\n%s", err, out)
		}
	}
	login("spare@example.com", "acct-2")
	remember()
	login("me@example.com", "acct-1")
	remember()
	if err := provider.SetLoginOn("codex", "me@example.com", false); err == nil {
		t.Fatal("turned off the account Codex is signed in to")
	}
	if err := provider.SetLoginOn("codex", "spare@example.com", true); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var tried []string
	upstream := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		mu.Unlock()
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"You've hit your usage limit"}}`)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), ".") {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	old := provider.CodexBase
	provider.CodexBase = upstream + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	s := gateway.New()
	code, body := send(s, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	mu.Lock()
	got := strings.Join(tried, ",")
	tried = nil
	mu.Unlock()
	if got != "acct-1,acct-2" {
		t.Fatalf("tried %v", got)
	}
	// the first sits out a minute; the next request goes straight on
	send(s, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("second request tried %v", tried)
	}
	// the agent's own sign-in is untouched by the saved account
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if !strings.Contains(string(b), "r-acct-1") || strings.Contains(string(b), "acct-2") {
		t.Fatalf("the agent's own credentials changed: %s", b)
	}
}
