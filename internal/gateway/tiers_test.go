package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat request rotates over the keys that are made for Chat (or name no
// protocol, as the first key may not); the keys made for another protocol
// come after the main pool, as asides.
func TestRotateOverMatchingKeysOnly(t *testing.T) {
	resetGroupTest(t)
	p := provider.Provider{ID: "rotchat", Name: "Rot", Key: "k1", KeyProtocol: provider.Chat, Routing: provider.Rotate,
		Chat: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1", Models: []string{"m"},
		Keys: []provider.KeyAccount{{Key: "k2", Protocol: provider.Chat}, {Key: "k3"}, {Key: "kA", Protocol: provider.Anthropic}}}
	s := &Server{}
	for round := 0; round < 3; round++ {
		cs, pl := s.planT(p, "m", provider.Chat, nil)
		if len(cs) != 4 || len(pl.order) != 4 {
			t.Fatalf("round %d: candidates %d, order %d", round, len(cs), len(pl.order))
		}
		main := map[string]bool{}
		for i := 0; i < 2; i++ {
			if pl.order[i].Aside {
				t.Fatalf("round %d: main candidate %d marked aside", round, i)
			}
			main[cs[i].p.Key] = true
		}
		if !main["k1"] || !main["k2"] {
			t.Fatalf("round %d: main pool %v, want the two Chat keys", round, main)
		}
		if cs[2].p.Key != "k3" || !pl.order[2].Aside {
			t.Errorf("round %d: third is %q aside=%v, want k3 as an aside", round, cs[2].p.Key, pl.order[2].Aside)
		}
		if cs[3].p.Key != "kA" || !pl.order[3].Aside {
			t.Errorf("round %d: last is %q aside=%v, want kA as an aside", round, cs[3].p.Key, pl.order[3].Aside)
		}
	}
}

// When the main pool fails, an aside (the Anthropic key, for a Chat request)
// is tried and answers.
func TestAsideUsedWhenMainPoolFails(t *testing.T) {
	resetGroupTest(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		seen = append(seen, r.URL.Path+" "+key)
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		// a translated request is always streamed to the vendor
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	}))
	defer srv.Close()
	p := provider.Provider{ID: "mainfail", Name: "Main Fail", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "k-chat", KeyProtocol: provider.Chat, Models: []string{"claude-opus-4-8"},
		Keys: []provider.KeyAccount{{Key: "k-ant", Protocol: provider.Anthropic}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"mainfail/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	want := []string{"/v1/chat/completions k-chat", "/v1/messages k-ant"}
	if code != 200 || len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("status %d body %q, upstream saw %q, want %q", code, body, seen, want)
	}
}

// A new conversation, or one whose record has no answer yet, is "first";
// a conversation whose last answerer is resting says so, in any mode.
func TestAffinityFirstAndResting(t *testing.T) {
	resetGroupTest(t)
	now := time.Now()
	p := provider.Provider{ID: "aff2", KeyProtocol: provider.Chat}
	cs := []candidate{{p: p, model: "m", rest: "aff2#a"}, {p: p, model: "m", rest: "aff2#b"}}
	if _, at := affine(cs, provider.Chat, "", "sid-new", "aff2/m", 1, false, now); at == nil || at.Why != "first" {
		t.Fatalf("new session: %+v", at)
	}
	// rotation touches a record before any answer: still first
	touchConversation("sid-blank", "aff2/m", now, nil)
	if _, at := affine(cs, provider.Chat, "", "sid-blank", "aff2/m", 1, true, now); at == nil || at.Why != "first" {
		t.Fatalf("blank record: %+v", at)
	}
	touchConversation("sid-old", "aff2/m", now, func(c *conversation) {
		c.last, c.at, c.cacheRead = candKey(cs[0]), now, 2048
	})
	restingUntil.Lock()
	restingUntil.m["aff2#a"] = now.Add(time.Minute)
	restingUntil.Unlock()
	for _, mode := range []string{"", "session"} {
		if _, at := affine(cs, provider.Chat, mode, "sid-old", "aff2/m", 1, true, now); at == nil || at.Why != "resting" || at.Kept {
			t.Fatalf("mode %q resting: %+v", mode, at)
		}
	}
}
