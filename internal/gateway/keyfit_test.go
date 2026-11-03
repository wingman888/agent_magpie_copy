package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// relayWithKeys is a relay with three keys: k1 and k2 made for no protocol,
// kc made for Chat only.
func relayWithKeys(url string) provider.Provider {
	return provider.Provider{ID: "relay", Name: "Relay", Chat: url + "/v1", Anthropic: url, Models: []string{"m"},
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "kc", Protocol: provider.Chat}}}
}

func TestKeyFitTiers(t *testing.T) {
	// a key made for no protocol ranks 1; the request's own protocol 0,
	// another's 2; a Claude model is at home on Anthropic, whatever it came in
	for _, c := range []struct {
		q     provider.Provider
		model string
		from  provider.Protocol
		want  int
	}{
		{provider.Provider{}, "m", provider.Chat, 1},
		{provider.Provider{KeyProtocol: provider.Chat}, "m", provider.Chat, 0},
		{provider.Provider{KeyProtocol: provider.Anthropic}, "m", provider.Chat, 2},
		{provider.Provider{KeyProtocol: provider.Anthropic}, "claude-x", provider.Chat, 0},
		{provider.Provider{KeyProtocol: provider.Chat}, "claude-x", provider.Anthropic, 2},
	} {
		if got := keyFit(c.q, c.model, c.from); got != c.want {
			t.Errorf("keyFit(%q, %s, %s) = %d, want %d", c.q.KeyProtocol, c.model, c.from, got, c.want)
		}
	}
}

// The keys that suit the request are the pool; the others come after it,
// marked aside, and the pool's order is the routing's.
func TestKeysSuitingTheRequestFirst(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	p := relayWithKeys("http://relay.test")
	k1, k2, kc := "relay#"+provider.KeyID("k1"), "relay#"+provider.KeyID("k2"), "relay#"+provider.KeyID("kc")
	s := New()
	for _, c := range []struct {
		from provider.Protocol
		want string // in order; "*" marks one aside
	}{
		// a Chat request: the Chat key is the pool, the two others aside
		{provider.Chat, kc + " " + k1 + "* " + k2 + "* "},
		// an Anthropic request: the two keys made for no protocol are the
		// pool, the Chat key aside and last
		{provider.Anthropic, k1 + " " + k2 + " " + kc + "* "},
	} {
		cs, pl := s.plan(p, "m", c.from)
		var got string
		for i, cand := range cs {
			got += cand.rest
			if cand.aside {
				got += "*"
			}
			got += " "
			if w := pl.order[i]; w.ID != cand.rest || w.Aside != cand.aside {
				t.Fatalf("%s: trace says %s aside=%v, candidate is %s aside=%v", c.from, w.ID, w.Aside, cand.rest, cand.aside)
			}
		}
		if got != c.want {
			t.Errorf("%s request: got %q, want %q", c.from, got, c.want)
		}
	}
}

// The keys aside are tried only when the pool fails: here both keys that
// suit an Anthropic request are rate limited, so the Chat key answers.
func TestAsideKeyTakenWhenThePoolFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	var mu sync.Mutex // seen is written by the server's goroutines
	var seen []string
	took := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(seen, ",")
	}
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		seen = nil
	}
	limited := map[string]bool{"k1": true, "k2": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if limited[key] {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		if r.URL.Path == "/v1/messages" {
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"from-`+key+`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		// a translated request is streamed upstream, whatever the agent asked
		if bytes.Contains(b, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from-`+key+`"},"finish_reason":null}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-`+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	if err := provider.Save(relayWithKeys(srv.URL)); err != nil {
		t.Fatal(err)
	}

	// a Chat request goes to the Chat key alone while it answers
	reset()
	if code, body := post(t, "/v1/chat/completions", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-kc") {
		t.Fatalf("chat: %d %s", code, body)
	}
	if got := took(); got != "/v1/chat/completions kc" {
		t.Fatalf("chat request tried %v", got)
	}

	// an Anthropic request tries the pool first, and only then the Chat key
	reset()
	if code, body := post(t, "/v1/messages", `{"model":"relay/m","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-kc") {
		t.Fatalf("anthropic: upstream saw %v; code %d; body %s", took(), code, strings.NewReplacer(`"`, `'`).Replace(strings.Join(strings.Fields(body), " ")))
	}
	if got := took(); got != "/v1/messages k1,/v1/messages k2,/v1/chat/completions kc" {
		t.Fatalf("anthropic request tried %v", got)
	}
}

// A provider's keys aside belong to it: its main pool, then its keys aside,
// and only then the next provider in the chain, whatever the next one has.
func TestAsideKeysStayWithTheirProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	relay := relayWithKeys("http://relay.test")
	relay.Fallback = []string{"spare/m"}
	spare := provider.Provider{ID: "spare", Name: "Spare", Key: "kb", Chat: "http://spare.test/v1", Models: []string{"m"}}
	for _, p := range []provider.Provider{relay, spare} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	k1, k2, kc := "relay#"+provider.KeyID("k1"), "relay#"+provider.KeyID("k2"), "relay#"+provider.KeyID("kc")
	s := New()
	order := func() string {
		cs, _ := s.plan(relay, "m", provider.Chat)
		var got string
		for _, c := range cs {
			got += c.rest
			if c.aside {
				got += "*"
			}
			got += " "
		}
		return got
	}
	// the Chat key is the relay's pool; its two others aside, then spare
	if got, want := order(), kc+" "+k1+"* "+k2+"* spare "; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// the relay's pool resting: spare is ready and goes first, the relay's
	// keys go to the back with the pool, its aside keys after it
	restingUntil.Lock()
	restingUntil.m[kc] = time.Now().Add(time.Minute)
	restingUntil.Unlock()
	defer func() {
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
	}()
	if got, want := order(), "spare "+kc+" "+k1+"* "+k2+"* "; got != want {
		t.Fatalf("resting: got %q, want %q", got, want)
	}
}
