package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fbUpstream is one vendor's API. It answers with code and msg while code is
// set, and with a normal reply otherwise. It records the model each request
// names, so a test can see what the upstream was asked for.
type fbUpstream struct {
	mu     sync.Mutex
	code   int
	msg    string
	models []string
}

func (u *fbUpstream) set(code int, msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.code, u.msg = code, msg
}

func (u *fbUpstream) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.models)
}

func (u *fbUpstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return ""
	}
	return u.models[len(u.models)-1]
}

// fbChatStream is a complete Chat Completions stream saying "ok".
var fbChatStream = sse(
	`data: {"id":"c1","model":"m2","choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	`data: [DONE]`)

func (u *fbUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.models = append(u.models, modelOf(body))
	code, msg := u.code, u.msg
	u.mu.Unlock()
	if code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, msg)
		return
	}
	switch r.URL.Path {
	case "/v1/messages":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicReply)
	case "/v1/chat/completions":
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, fbChatStream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	default:
		http.NotFound(w, r)
	}
}

// fallbackSetup isolates the config and saves two providers: alpha, asked for
// model m1 and holding the given fallback list, and beta, serving m2.
func fallbackSetup(t *testing.T, fallback ...string) (alpha, beta *fbUpstream) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	alpha, beta = &fbUpstream{}, &fbUpstream{}
	ua := httptest.NewServer(alpha)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(beta)
	t.Cleanup(ub.Close)
	mustDo(t, provider.Save(provider.Provider{ID: "alpha", Name: "Alpha", Key: "ka", Chat: ua.URL + "/v1",
		Models: []string{"m1"}, Fallback: fallback}))
	mustDo(t, provider.Save(provider.Provider{ID: "beta", Name: "Beta", Key: "kb", Chat: ub.URL + "/v1",
		Models: []string{"m2"}}))
	return alpha, beta
}

func TestFallbackAnswersWhenPrimaryIsLimited(t *testing.T) {
	cases := []struct {
		name string
		code int
		msg  string
	}{
		{"rate limit", 429, `{"error":{"message":"slow down"}}`},
		{"payment required", 402, `{"error":{"message":"pay up"}}`},
		{"overloaded", 529, `{"error":{"message":"busy"}}`},
		{"quota 403", 403, `{"error":{"message":"quota exceeded for this key"}}`},
		{"unavailable", 503, `{"error":{"message":"down for maintenance"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			alpha, beta := fallbackSetup(t, "beta/m2")
			alpha.set(c.code, c.msg)
			srv := New()
			code, body := callOn(srv, "/v1/chat/completions", chatBody)
			if code != 200 || !strings.Contains(body, "ok") {
				t.Fatalf("status %d %s, want the fallback's reply", code, body)
			}
			if beta.hits() != 1 || beta.lastModel() != "m2" {
				t.Fatalf("beta hits=%d model=%q, want one call for m2", beta.hits(), beta.lastModel())
			}
			if alpha.lastModel() != "m1" {
				t.Fatalf("alpha was asked for %q, want m1", alpha.lastModel())
			}
			calls := srv.Recent()
			if calls[0].Provider != "beta" || calls[0].Status != 200 {
				t.Fatalf("call %+v, want beta answering with 200", calls[0])
			}
			if calls[0].Fallback != "alpha" {
				t.Fatalf("fallback = %q, want alpha", calls[0].Fallback)
			}
		})
	}
}

func TestBadRequestIsNotFallenBack(t *testing.T) {
	alpha, beta := fallbackSetup(t, "beta/m2")
	alpha.set(400, `{"error":{"message":"bad param"}}`)
	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, "bad param") {
		t.Fatalf("status %d %s, want the 400 as it came", code, body)
	}
	if beta.hits() != 0 {
		t.Fatalf("beta was tried %d times for a 400", beta.hits())
	}
	if calls := srv.Recent(); calls[0].Fallback != "" || calls[0].Provider != "alpha" {
		t.Fatalf("call %+v, want alpha and no fallback", calls[0])
	}
}

func TestLastCandidateErrorReachesClient(t *testing.T) {
	alpha, beta := fallbackSetup(t, "beta/m2")
	alpha.set(429, `{"error":{"message":"slow down"}}`)
	beta.set(503, `{"error":{"message":"down for maintenance"}}`)
	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", chatBody)
	if code != 503 || !strings.Contains(body, "Beta: down for maintenance") {
		t.Fatalf("status %d %s, want beta's 503 as it came", code, body)
	}
	if alpha.hits() != 1 || beta.hits() != 1 {
		t.Fatalf("hits alpha=%d beta=%d, want 1 and 1", alpha.hits(), beta.hits())
	}
	// both candidates failed, so both are named
	if calls := srv.Recent(); calls[0].Status != 503 || calls[0].Provider != "beta" ||
		!strings.Contains(calls[0].Fallback, "alpha") || !strings.Contains(calls[0].Fallback, "beta") {
		t.Fatalf("call %+v", calls[0])
	}
}

func TestFailedProviderRestsForAMinute(t *testing.T) {
	alpha, beta := fallbackSetup(t, "beta/m2")
	alpha.set(429, `{"error":{"message":"slow down"}}`)
	srv := New()
	for i := 0; i < 2; i++ {
		if code, body := callOn(srv, "/v1/chat/completions", chatBody); code != 200 {
			t.Fatalf("request %d: %d %s", i+1, code, body)
		}
	}
	if alpha.hits() != 1 || beta.hits() != 2 {
		t.Fatalf("hits alpha=%d beta=%d, want 1 and 2: alpha should rest", alpha.hits(), beta.hits())
	}
	if calls := srv.Recent(); calls[0].Fallback != "" {
		t.Fatalf("second request fell back: %q", calls[0].Fallback)
	}
	// once the rest is over, alpha is asked first again
	srv.mu.Lock()
	srv.cool[providerCoolKey("alpha")] = time.Now().Add(-time.Second)
	srv.mu.Unlock()
	if code, body := callOn(srv, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("after rest: %d %s", code, body)
	}
	if alpha.hits() != 2 {
		t.Fatalf("alpha hits %d after its rest ended, want 2", alpha.hits())
	}
}

func TestFallbackAnswersStreamedRequest(t *testing.T) {
	alpha, beta := fallbackSetup(t, "beta/m2")
	alpha.set(429, `{"error":{"message":"slow down"}}`)
	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("status %d %s, want the fallback's stream", code, body)
	}
	if beta.lastModel() != "m2" {
		t.Fatalf("beta asked for %q, want m2", beta.lastModel())
	}
}

func TestFallbackTranslatesForOtherClient(t *testing.T) {
	alpha, beta := fallbackSetup(t, "beta/m2")
	alpha.set(529, `{"error":{"message":"busy"}}`)
	srv := New()
	code, body := callOn(srv, "/v1/messages", `{"model":"m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("status %d %s, want the translated fallback reply", code, body)
	}
	if beta.lastModel() != "m2" {
		t.Fatalf("beta asked for %q, want m2", beta.lastModel())
	}
	if calls := srv.Recent(); calls[0].Provider != "beta" || calls[0].To != provider.Chat {
		t.Fatalf("call %+v, want beta over chat", calls[0])
	}
}

func TestUnusableFallbackEntriesAreSkipped(t *testing.T) {
	alpha, beta := fallbackSetup(t, "nonsense", "/m2", "beta/", "ghost/m3", "alpha/m1", "beta/m2")
	alpha.set(429, `{"error":{"message":"slow down"}}`)
	srv := New()
	if code, body := callOn(srv, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("status %d %s", code, body)
	}
	if beta.hits() != 1 || srv.Recent()[0].Provider != "beta" {
		t.Fatalf("beta hits=%d, want the one usable entry", beta.hits())
	}
}

func TestPrimaryIsNotItsOwnFallback(t *testing.T) {
	alpha, _ := fallbackSetup(t, "alpha/m1")
	alpha.set(429, `{"error":{"message":"slow down"}}`)
	code, _ := callOn(New(), "/v1/chat/completions", chatBody)
	if code != 429 {
		t.Fatalf("status %d, want the primary's 429", code)
	}
	if alpha.hits() != 1 {
		t.Fatalf("alpha tried %d times, want once", alpha.hits())
	}
}
