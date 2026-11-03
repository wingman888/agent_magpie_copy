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

// counter is an upstream that answers every request with one fixed status
// and body, and counts the requests each key received.
type counter struct {
	mu     sync.Mutex
	status int
	body   string
	hits   map[string]int
}

func (c *counter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	c.mu.Lock()
	c.hits[key]++
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.status)
	io.WriteString(w, c.body)
}

func (c *counter) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[key]
}

// saveNamed saves provider "plan" with the given display name, the first key
// as its front key, the rest added through AddKey, and the given fallbacks.
func saveNamed(t *testing.T, name, url string, fallback []string, keys ...acct) {
	t.Helper()
	first := keys[0]
	p := provider.Provider{ID: "plan", Name: name, Key: first.key, KeyName: first.name,
		Models: []string{"m1", "m3"}, Fallback: fallback, Chat: url + "/v1", Anthropic: url + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, a := range keys[1:] {
		if err := provider.AddKey("plan", a.name, a.key); err != nil {
			t.Fatal(err)
		}
	}
}

// countRequest is a count_tokens request for the test provider's model.
func countRequest() (*httptest.ResponseRecorder, *http.Request) {
	body := `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`
	return httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body))
}

// Both keys are refused and the refusal echoes the key: the client must see
// neither full key, only their masks or nothing.
func TestClientNeverSeesAFullKey(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429, secretTeam: 429})
	planWith(t, serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	code, body := sendChat(New(), chatReq)
	if code != 429 {
		t.Fatalf("status %d, want 429: %s", code, body)
	}
	for _, k := range []string{secretFirst, secretTeam} {
		if strings.Contains(body, k) {
			t.Fatalf("full key %s in client reply: %s", k, body)
		}
	}
	if !strings.Contains(body, "slow down") {
		t.Fatalf("client reply lost the reason: %s", body)
	}
}

// A 400 whose body is an ordinary invalid-model error is not a quota failure,
// even when the provider's own name contains "credit" or "billing": the
// gateway judges the upstream body, so nothing rests and no other key is tried.
func TestProviderNameDoesNotMakeAFailureRetryable(t *testing.T) {
	newEnv(t)
	up := &counter{status: 400, body: `{"error":{"message":"invalid model"}}`, hits: map[string]int{}}
	saveNamed(t, "Credit Billing Plan", serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	s := New()
	code, body := sendChat(s, chatReq)
	if code != 400 || !strings.Contains(body, "invalid model") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count(secretFirst) != 1 || up.count(secretTeam) != 0 {
		t.Fatalf("hits personal=%d team=%d, want 1 and 0", up.count(secretFirst), up.count(secretTeam))
	}
	restingUntil.Lock()
	n := len(restingUntil.m)
	restingUntil.Unlock()
	if n != 0 {
		t.Fatalf("%d keys rest after a non-quota failure", n)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("non-quota failure was recorded as a skipped try: %q", c.Fallback)
	}
}

// A failed key does not come back under another model of the same provider
// within the same request: the fallback model's tries for it are dropped.
func TestFailedKeyIsNotRetriedUnderFallbackModel(t *testing.T) {
	newEnv(t)
	up := &counter{status: 429, body: `{"error":{"message":"slow down"}}`, hits: map[string]int{}}
	saveNamed(t, "Plan", serveAt(t, up), []string{"plan/m3"}, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	code, _ := sendChat(New(), chatReq)
	if code != 429 {
		t.Fatalf("status %d, want 429", code)
	}
	if up.count(secretFirst) != 1 || up.count(secretTeam) != 1 {
		t.Fatalf("hits personal=%d team=%d, want 1 and 1", up.count(secretFirst), up.count(secretTeam))
	}
}

// After the front key is refused, count_tokens goes to the key that is not
// resting, not back to the refused one.
func TestCountTokensSkipsRestingKey(t *testing.T) {
	newEnv(t)
	up := newKeyed(map[string]int{secretFirst: 429})
	saveNamed(t, "Plan", serveAt(t, up), nil, acct{"个人", secretFirst}, acct{"团队", secretTeam})
	s := New()
	if code, body := sendChat(s, chatReq); code != 200 || !strings.Contains(body, "answer-ok") {
		t.Fatalf("%d %s", code, body)
	}
	rec, req := countRequest()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("count_tokens %d %s", rec.Code, rec.Body.String())
	}
	if up.count(secretFirst) != 1 || up.count(secretTeam) != 2 {
		t.Fatalf("hits personal=%d team=%d, want 1 and 2", up.count(secretFirst), up.count(secretTeam))
	}
}
