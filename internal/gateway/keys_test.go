package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyFake answers by the key a request carries: with the status given for
// that key, or 200. It records "key path" for every request it gets.
type keyFake struct {
	mu     sync.Mutex
	status map[string]int
	hits   []string
}

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	f.mu.Lock()
	f.hits = append(f.hits, key+" "+r.URL.Path)
	code := f.status[key]
	f.mu.Unlock()
	if code == 0 {
		code = 200
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if code >= 400 {
		io.WriteString(w, `{"error":{"message":"rate limit reached"}}`)
		return
	}
	io.WriteString(w, `{"id":"x","ok":true}`)
}

func (f *keyFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func postWith(t *testing.T, g *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func setupKeys(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// TestKeysFailOverWithinOneRequest: the first key is rate limited, so the
// second answers the same request; the next request goes to the second key
// straight away, while the first waits out its cooldown.
func TestKeysFailOverWithinOneRequest(t *testing.T) {
	up := &keyFake{status: map[string]int{"k1": 429}}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	setupKeys(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal",
		Chat: upstream.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	g := New()
	code, body := postWith(t, g, "/v1/chat/completions", `{"model":"relay/m1","messages":[]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := up.seen(); len(got) != 2 || got[0] != "k1 /v1/chat/completions" || got[1] != "k2 /v1/chat/completions" {
		t.Fatalf("upstream saw %v", got)
	}
	calls := g.Recent()
	if calls[0].Status != 200 || calls[0].Provider != "relay" || calls[0].Fallback != "relay (key personal)" {
		t.Fatalf("call = %+v", calls[0])
	}

	postWith(t, g, "/v1/chat/completions", `{"model":"relay/m1","messages":[]}`)
	got := up.seen()
	if len(got) != 3 || got[2] != "k2 /v1/chat/completions" {
		t.Fatalf("second request should go straight to k2, upstream saw %v", got)
	}
	if g.Recent()[0].Fallback != "" {
		t.Fatalf("a first-try success must record no fallback, got %q", g.Recent()[0].Fallback)
	}
}

// TestBadRequestIsNotRetried: a 400 is the request's own fault; the next key
// would fail the same way, so the error goes back as it is.
func TestBadRequestIsNotRetried(t *testing.T) {
	up := &keyFake{status: map[string]int{"k1": 400}}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	setupKeys(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1",
		Chat: upstream.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	g := New()
	code, _ := postWith(t, g, "/v1/chat/completions", `{"model":"relay/m1","messages":[]}`)
	if code != 400 {
		t.Fatalf("status %d, want 400", code)
	}
	if got := up.seen(); len(got) != 1 {
		t.Fatalf("upstream saw %v; a 400 must not move to another key", got)
	}
	if g.Recent()[0].Fallback != "" {
		t.Fatalf("fallback = %q, want none", g.Recent()[0].Fallback)
	}
}

// TestKeyProtocolLimitsEndpoints: a key limited to Anthropic answers Claude
// Code's request on the Anthropic endpoint; the OpenAI-limited key is never
// reached, even though it is first in the file.
func TestKeyProtocolLimitsEndpoints(t *testing.T) {
	up := &keyFake{}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	setupKeys(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "c1", KeyName: "oai",
		KeyProtocol: provider.Chat, Chat: upstream.URL + "/v1", Anthropic: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "ant", "a1", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	g := New()
	code, body := postWith(t, g, "/v1/messages", `{"model":"relay/claude-x","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := up.seen(); len(got) != 1 || got[0] != "a1 /v1/messages" {
		t.Fatalf("upstream saw %v, want only the Anthropic key on /v1/messages", got)
	}
}

// TestPlanOrdersByProtocol: a Claude model goes to the Anthropic key first,
// and a GPT model from an Anthropic client goes to the OpenAI key first, with
// the Anthropic key as the translated last resort.
func TestPlanOrdersByProtocol(t *testing.T) {
	p := provider.Provider{ID: "relay", Chat: "https://r.test/v1", Anthropic: "https://r.test",
		Key: "c1", KeyName: "oai", KeyProtocol: provider.Chat,
		Keys: []provider.ExtraKey{{Name: "ant", Key: "a1", Protocol: provider.Anthropic}}}
	g := New()

	cs := g.plan(p, provider.Chat, "claude-x", nil)
	if len(cs) != 2 || cs[0].p.KeyName != "ant" || cs[0].to != provider.Anthropic || cs[0].relay {
		t.Fatalf("claude from chat: %+v", cs)
	}

	cs = g.plan(p, provider.Anthropic, "gpt-5", nil)
	if len(cs) != 2 || cs[0].p.KeyName != "oai" || cs[0].to != provider.Chat || cs[0].relay {
		t.Fatalf("gpt from anthropic: first %+v", cs)
	}
	if cs[1].p.KeyName != "ant" || cs[1].to != provider.Anthropic || !cs[1].relay {
		t.Fatalf("gpt from anthropic: second %+v", cs[1])
	}
}

func TestFailoverClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{429, "", true}, {402, "", true}, {529, "", true}, {503, "", true},
		{403, `{"error":"quota exceeded for this key"}`, true},
		{403, `{"error":"forbidden"}`, false},
		{400, "bad parameter", false}, {401, "", false}, {404, "", false},
	} {
		if got := failover(c.status, []byte(c.body)); got != c.want {
			t.Errorf("failover(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
