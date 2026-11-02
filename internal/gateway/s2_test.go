package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type hit struct {
	path, key, model string
}

// upstream is a scripted vendor: fn decides each reply from the hit.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []hit
	fn   func(h hit) (status int, ctype, body string)
}

func newUpstream(t *testing.T, fn func(h hit) (int, string, string)) *upstream {
	t.Helper()
	u := &upstream{fn: fn}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if k := r.Header.Get("x-api-key"); k != "" {
			key = k
		}
		h := hit{r.URL.Path, key, b.Model}
		u.mu.Lock()
		u.hits = append(u.hits, h)
		u.mu.Unlock()
		status, ctype, body := fn(h)
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) log() []hit {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]hit(nil), u.hits...)
}

func (u *upstream) count() int { return len(u.log()) }

func (u *upstream) keys() string {
	var ks []string
	for _, h := range u.log() {
		ks = append(ks, h.key)
	}
	return strings.Join(ks, ",")
}

const okJSON = `{"id":"c1","choices":[{"message":{"content":"ok"}}]}`

func jsonOK(h hit) (int, string, string) { return 200, "application/json", okJSON }

func env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func send(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func mustSave(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func chatBody(model string, stream bool) string {
	b, _ := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	return string(b)
}

func TestKeysRelayInTurn(t *testing.T) {
	env(t)
	up := newUpstream(t, func(h hit) (int, string, string) {
		if h.key == "k1" {
			return 429, "application/json", `{"error":{"message":"slow down"}}`
		}
		return jsonOK(h)
	})
	mustSave(t, provider.Provider{ID: "fake", Name: "Fake", Chat: up.srv.URL + "/v1", Key: "k1", KeyName: "personal", Models: []string{"m1"}})
	for _, k := range [][2]string{{"team", "k2"}, {"spare", "k3"}} {
		if err := provider.AddKey("fake", k[0], k[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetKeyOn("fake", provider.KeyRef("k3"), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	code, body := send(h, "/v1/chat/completions", chatBody("fake/m1", false))
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.keys(); got != "k1,k2" {
		t.Fatalf("keys tried: %s", got)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "fake") || !strings.Contains(c.Fallback, "personal") || strings.Contains(c.Fallback, "team") || c.Provider != "fake" {
		t.Errorf("call: %+v", c)
	}
	// the next request goes straight to the second key; the third is off
	send(h, "/v1/chat/completions", chatBody("fake/m1", false))
	if got := up.keys(); got != "k1,k2,k2" {
		t.Fatalf("keys tried: %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("fallback %q", c.Fallback)
	}
}

func TestKeyManagement(t *testing.T) {
	env(t)
	mustSave(t, provider.Provider{ID: "fake", Name: "Fake", Chat: "http://x/v1", Key: "k1", KeyName: "a", KeyProtocol: provider.Chat})
	if err := provider.AddKey("fake", "b", "k2", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	if provider.AddKey("fake", "dup", "k2", "") == nil || provider.AddKey("fake", "x", "k9", "gemini") == nil {
		t.Fatal("duplicate / unknown protocol accepted")
	}
	if err := provider.UseKey("fake", provider.KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("fake")
	if p.Key != "k2" || p.KeyName != "b" || p.KeyProtocol != provider.Anthropic || len(p.Keys) != 1 ||
		p.Keys[0].Key != "k1" || p.Keys[0].Name != "a" || p.Keys[0].Protocol != provider.Chat || p.Keys[0].Off {
		t.Fatalf("%+v", p)
	}
	// switching off the first hands over to the next, which leaves k2 off but kept
	if err := provider.SetKeyOn("fake", provider.KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("fake")
	if p.Key != "k1" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || !p.Keys[0].Off {
		t.Fatalf("%+v", p)
	}
	if provider.SetKeyOn("fake", provider.KeyRef("k1"), false) == nil || provider.RemoveKey("fake", provider.KeyRef("k1")) == nil {
		t.Fatal("last key that is on was touched")
	}
	if err := provider.RemoveKey("fake", provider.KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("fake", provider.KeyRef("k1"), "main"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("fake", provider.KeyRef("k1"), ""); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("fake")
	if p.KeyName != "main" || p.KeyProtocol != "" || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
	if provider.Save(provider.Provider{ID: "bad", Chat: "http://x/v1", Key: "k", KeyProtocol: "nope"}) == nil {
		t.Fatal("unknown key protocol saved")
	}
}

const anthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
const chatSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"

func TestKeyProtocolSelection(t *testing.T) {
	anthropicBody := `{"model":"MODEL","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	for _, order := range []string{"anthropic-first", "openai-first"} {
		t.Run(order, func(t *testing.T) {
			env(t)
			up := newUpstream(t, func(h hit) (int, string, string) {
				if h.path == "/v1/messages" {
					return 200, "text/event-stream", anthropicSSE
				}
				return 200, "text/event-stream", chatSSE
			})
			p := provider.Provider{ID: "relay", Name: "Relay", Chat: up.srv.URL + "/v1", Anthropic: up.srv.URL, Models: []string{"claude-x", "gpt-5"}}
			ka := provider.ExtraKey{Key: "ka", Protocol: provider.Anthropic}
			ko := provider.ExtraKey{Key: "ko", Protocol: provider.Chat}
			if order == "anthropic-first" {
				p.Key, p.KeyProtocol, p.Keys = ka.Key, ka.Protocol, []provider.ExtraKey{ko}
			} else {
				p.Key, p.KeyProtocol, p.Keys = ko.Key, ko.Protocol, []provider.ExtraKey{ka}
			}
			mustSave(t, p)
			h := New().Handler()
			expect := func(what string, wantPath, wantKey string) {
				t.Helper()
				hs := up.log()
				if len(hs) != 1 || hs[0].path != wantPath || hs[0].key != wantKey {
					t.Fatalf("%s: %+v", what, hs)
				}
				up.mu.Lock()
				up.hits = nil
				up.mu.Unlock()
			}
			if code, b := send(h, "/v1/messages", strings.Replace(anthropicBody, "MODEL", "relay/claude-x", 1)); code != 200 {
				t.Fatalf("%d %s", code, b)
			}
			expect("claude code, claude model", "/v1/messages", "ka")
			if code, b := send(h, "/v1/chat/completions", chatBody("relay/claude-x", true)); code != 200 {
				t.Fatalf("%d %s", code, b)
			}
			expect("openai client, claude model", "/v1/messages", "ka")
			if code, b := send(h, "/v1/messages", strings.Replace(anthropicBody, "MODEL", "relay/gpt-5", 1)); code != 200 {
				t.Fatalf("%d %s", code, b)
			}
			expect("claude code, gpt model", "/v1/chat/completions", "ko")
		})
	}
}

func TestKeyWithoutEndpointSitsOut(t *testing.T) {
	env(t)
	up := newUpstream(t, jsonOK)
	mustSave(t, provider.Provider{ID: "fake", Name: "Fake", Chat: up.srv.URL + "/v1", Models: []string{"m1"},
		Key: "ka", KeyProtocol: provider.Anthropic, Keys: []provider.ExtraKey{{Key: "kc"}}})
	if code, b := send(New().Handler(), "/v1/chat/completions", chatBody("fake/m1", false)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if got := up.keys(); got != "kc" {
		t.Fatalf("keys: %s", got)
	}
}

func TestCountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	env(t)
	up := newUpstream(t, func(h hit) (int, string, string) {
		return 200, "application/json", `{"input_tokens":7}`
	})
	mustSave(t, provider.Provider{ID: "relay", Name: "Relay", Chat: up.srv.URL + "/v1", Anthropic: up.srv.URL, Models: []string{"claude-x"},
		Key: "ko", KeyProtocol: provider.Chat, Keys: []provider.ExtraKey{{Key: "ka", Protocol: provider.Anthropic}}})
	code, body := send(New().Handler(), "/v1/messages/count_tokens", `{"model":"relay/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"input_tokens":7`) {
		t.Fatalf("%d %s", code, body)
	}
	if hs := up.log(); len(hs) != 1 || hs[0].key != "ka" || hs[0].path != "/v1/messages/count_tokens" {
		t.Fatalf("%+v", hs)
	}
}

// fallbackSetup makes provider "main" (m1) with "alt/m2" as its fallback.
func fallbackSetup(t *testing.T, mainFn, altFn func(h hit) (int, string, string)) (*upstream, *upstream, *Server) {
	t.Helper()
	env(t)
	mainUp, altUp := newUpstream(t, mainFn), newUpstream(t, altFn)
	mustSave(t, provider.Provider{ID: "alt", Name: "Alt", Chat: altUp.srv.URL + "/v1", Key: "ak", Models: []string{"m2"}})
	mustSave(t, provider.Provider{ID: "main", Name: "Main", Chat: mainUp.srv.URL + "/v1", Key: "mk", Models: []string{"m1"},
		Fallback: []string{"nope/x", "alt/m2", "alt/m2"}})
	return mainUp, altUp, New()
}

func fail(status int, body string) func(h hit) (int, string, string) {
	return func(h hit) (int, string, string) { return status, "application/json", body }
}

func TestFallbackOnQuotaErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`},
		{"402", 402, `{"error":{"message":"pay up"}}`},
		{"529", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{"503", 503, `unavailable`},
		{"403 quota", 403, `{"error":{"message":"额度已用完"}}`},
		{"403 quota en", 403, `{"error":{"message":"Your quota is exhausted"}}`},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				mainUp, altUp, s := fallbackSetup(t, fail(tc.status, tc.body), jsonOK)
				code, body := send(s.Handler(), "/v1/chat/completions", chatBody("main/m1", stream))
				if code != 200 || !strings.Contains(body, `"ok"`) {
					t.Fatalf("%d %s", code, body)
				}
				if h := mainUp.log(); len(h) != 1 || h[0].model != "m1" {
					t.Errorf("main: %+v", h)
				}
				if h := altUp.log(); len(h) != 1 || h[0].model != "m2" || h[0].key != "ak" {
					t.Errorf("alt: %+v", h)
				}
				c := s.Recent()[0]
				if c.Provider != "alt" || !strings.Contains(c.Fallback, "main") {
					t.Errorf("call: %+v", c)
				}
			})
		}
	}
}

func TestNoFallbackForOrdinaryErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{400, `{"error":{"message":"bad parameter"}}`}, {403, `{"error":{"message":"forbidden"}}`}, {401, `{"error":{"message":"bad key"}}`}, {500, `boom`}} {
		mainUp, altUp, s := fallbackSetup(t, fail(tc.status, tc.body), jsonOK)
		code, body := send(s.Handler(), "/v1/chat/completions", chatBody("main/m1", false))
		if code != tc.status || !strings.Contains(body, strings.Trim(tc.body[strings.LastIndex(tc.body, ":")+1:], `"{}`)) {
			t.Errorf("%d: got %d %s", tc.status, code, body)
		}
		if altUp.count() != 0 || mainUp.count() != 1 {
			t.Errorf("%d: alt %d main %d", tc.status, altUp.count(), mainUp.count())
		}
		if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "main" {
			t.Errorf("call %+v", c)
		}
	}
}

func TestAllCandidatesFail(t *testing.T) {
	_, _, s := fallbackSetup(t, fail(429, `{"error":{"message":"main limited"}}`), fail(529, `{"error":{"message":"alt overloaded"}}`))
	code, body := send(s.Handler(), "/v1/chat/completions", chatBody("main/m1", false))
	if code != 529 || !strings.Contains(body, "alt overloaded") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Status != 529 || c.Provider != "alt" || !strings.Contains(c.Fallback, "main") {
		t.Errorf("call %+v", c)
	}
}

func TestFailedCandidateRests(t *testing.T) {
	mainUp, altUp, s := fallbackSetup(t, fail(429, `{"error":{"message":"limited"}}`), jsonOK)
	now := time.Now()
	s.clock = func() time.Time { return now }
	h := s.Handler()
	send(h, "/v1/chat/completions", chatBody("main/m1", false))
	send(h, "/v1/chat/completions", chatBody("main/m1", false))
	if mainUp.count() != 1 || altUp.count() != 2 {
		t.Fatalf("main %d alt %d", mainUp.count(), altUp.count())
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "alt" {
		t.Errorf("call %+v", c)
	}
	now = now.Add(61 * time.Second)
	send(h, "/v1/chat/completions", chatBody("main/m1", false))
	if mainUp.count() != 2 || altUp.count() != 3 {
		t.Fatalf("after rest: main %d alt %d", mainUp.count(), altUp.count())
	}
}

func TestStreamStartErrorMovesOn(t *testing.T) {
	sseErr := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	mainUp, altUp, s := fallbackSetup(t,
		func(h hit) (int, string, string) { return 200, "text/event-stream", sseErr },
		func(h hit) (int, string, string) { return 200, "text/event-stream", chatSSE })
	code, body := send(s.Handler(), "/v1/chat/completions", chatBody("main/m1", true))
	if code != 200 || strings.Contains(body, "Overloaded") || !strings.Contains(body, "hi") {
		t.Fatalf("%d %s", code, body)
	}
	if mainUp.count() != 1 || altUp.count() != 1 {
		t.Errorf("main %d alt %d", mainUp.count(), altUp.count())
	}
}

func TestKeysBeforeFallback(t *testing.T) {
	mainUp, altUp, s := fallbackSetup(t, func(h hit) (int, string, string) {
		if h.key == "mk" {
			return 429, "application/json", `{"error":{"message":"limited"}}`
		}
		return jsonOK(h)
	}, jsonOK)
	if err := provider.AddKey("main", "team", "mk2", ""); err != nil {
		t.Fatal(err)
	}
	code, _ := send(s.Handler(), "/v1/chat/completions", chatBody("main/m1", false))
	if code != 200 || mainUp.keys() != "mk,mk2" || altUp.count() != 0 {
		t.Fatalf("%d main %s alt %d", code, mainUp.keys(), altUp.count())
	}
}
