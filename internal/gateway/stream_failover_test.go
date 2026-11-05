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

// streamKeyFake answers a streaming chat request with HTTP 200 and the SSE
// body given for the key the request carries. It counts the requests per key.
type streamKeyFake struct {
	mu     sync.Mutex
	bodies map[string]string
	hits   []string
}

func (f *streamKeyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.hits = append(f.hits, key)
	body := f.bodies[key]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, body)
}

func (f *streamKeyFake) seenKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

const overloadedStream = "data: {\"error\":{\"message\":\"Overloaded\",\"type\":\"overloaded_error\"}}\n\n"

const okChatStream = "data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"second ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

// setupStreamKeys saves a provider with two keys on a Chat endpoint.
func setupStreamKeys(t *testing.T, f *streamKeyFake, first string) {
	t.Helper()
	upstream := httptest.NewServer(f)
	t.Cleanup(upstream.Close)
	setupKeys(t)
	f.bodies = map[string]string{"k1": first, "k2": okChatStream}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal",
		Chat: upstream.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
}

// An Anthropic client streaming to a Chat-only provider is translated. The
// first key opens its reply with an overloaded error; the client must not see
// it, and the second key's reply must arrive whole.
func TestStreamOpeningErrorMovesToNextKeyTranslated(t *testing.T) {
	f := &streamKeyFake{}
	setupStreamKeys(t, f, overloadedStream)
	g := New()
	code, body := postWith(t, g, "/v1/messages", `{"model":"relay/m1","max_tokens":1,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, "Overloaded") {
		t.Fatalf("client saw the opening error: %s", body)
	}
	if !strings.Contains(body, "second ok") {
		t.Fatalf("client did not get the second key's reply: %s", body)
	}
	if got := f.seenKeys(); len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("upstream saw %v, want k1 then k2", got)
	}
	if c := g.Recent()[0]; c.Fallback != "relay (key personal)" || c.Provider != "relay" {
		t.Fatalf("call = %+v", c)
	}
}

// A Chat client streaming to the same provider is relayed as it is; the
// opening error must be held back and the second key used instead.
func TestStreamOpeningErrorMovesToNextKeyRelayed(t *testing.T) {
	f := &streamKeyFake{}
	setupStreamKeys(t, f, overloadedStream)
	g := New()
	code, body := postWith(t, g, "/v1/chat/completions", `{"model":"relay/m1","stream":true,"messages":[]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, "overloaded") || strings.Contains(body, "Overloaded") {
		t.Fatalf("client saw the opening error: %s", body)
	}
	if !strings.Contains(body, "second ok") {
		t.Fatalf("client did not get the second key's reply: %s", body)
	}
	if got := f.seenKeys(); len(got) != 2 || got[1] != "k2" {
		t.Fatalf("upstream saw %v, want k1 then k2", got)
	}
}

// An opening error that is not a spent-quota wording is no reason to move on:
// it reaches the client as it is, and the second key is never tried.
func TestStreamOpeningBadRequestIsNotRetried(t *testing.T) {
	bad := "data: {\"error\":{\"message\":\"messages: field required\",\"type\":\"invalid_request_error\"}}\n\n"
	f := &streamKeyFake{}
	setupStreamKeys(t, f, bad)
	g := New()
	code, body := postWith(t, g, "/v1/chat/completions", `{"model":"relay/m1","stream":true,"messages":[]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "field required") {
		t.Fatalf("client should see the upstream error: %s", body)
	}
	if got := f.seenKeys(); len(got) != 1 {
		t.Fatalf("upstream saw %v; a bad request must not move to another key", got)
	}
}
