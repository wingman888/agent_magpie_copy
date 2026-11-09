package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyxUpstream answers by the key it was sent: a key listed in fail gets
// that status, every other key a plain JSON reply. It records "path key" for
// each request, in order.
type keyxUpstream struct {
	mu   sync.Mutex
	fail map[string]int
	hits []string
}

func (u *keyxUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	u.mu.Lock()
	u.hits = append(u.hits, r.URL.Path+" "+key)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code, bad := u.fail[key]; bad {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *keyxUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.hits...)
}

// keyxServe starts the upstream and points magpie's config at a temp dir.
func keyxServe(t *testing.T, fail map[string]int) (*keyxUpstream, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &keyxUpstream{fail: fail}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return up, srv
}

func keyxPost(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func keyxCheckHits(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
}

func TestKeyxFirstKeyRateLimitedUsesSecond(t *testing.T) {
	up, srv := keyxServe(t, map[string]int{"k1": 429})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal", Chat: srv.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`

	if code, out := keyxPost(s, "/v1/chat/completions", body); code != 200 {
		t.Fatalf("first request: %d %s", code, out)
	}
	first := s.Recent()[0]
	if first.Status != 200 || first.Provider != "relay" || first.Fallback != "relay (personal)" {
		t.Fatalf("call record: %+v", first)
	}

	// the rate-limited key is resting, so the next request goes straight to k2
	if code, out := keyxPost(s, "/v1/chat/completions", body); code != 200 {
		t.Fatalf("second request: %d %s", code, out)
	}
	if last := s.Recent()[0]; last.Fallback != "" {
		t.Fatalf("second request should need no fallback: %+v", last)
	}
	keyxCheckHits(t, up.seen(), "/v1/chat/completions k1", "/v1/chat/completions k2", "/v1/chat/completions k2")
}

func TestKeyxDisabledKeyNeverTried(t *testing.T) {
	up, srv := keyxServe(t, map[string]int{"k1": 429})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: srv.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", provider.KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	body := `{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`
	code, _ := keyxPost(s, "/v1/chat/completions", body)
	if code != 429 {
		t.Fatalf("the only enabled key failed with 429; client should see it, got %d", code)
	}
	keyxCheckHits(t, up.seen(), "/v1/chat/completions k1")
}

func TestKeyxProtocolLimitedKeysRouteByModel(t *testing.T) {
	// both keys are refused with 400, which is not a failover reason, so each
	// request makes exactly one try and the hit shows where it went.
	up, srv := keyxServe(t, map[string]int{"oai-key": 400, "ant-key": 400})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "oai-key", KeyProtocol: provider.Chat,
		Chat: srv.URL + "/v1", Anthropic: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// the Anthropic key is added second; order must not matter
	if err := provider.AddKey("relay", "ant", "ant-key", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	s := New()
	claude := `{"model":"relay/claude-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	gpt := `{"model":"relay/gpt-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

	// Claude Code asks for a Claude model: the Anthropic key on the Anthropic endpoint
	keyxPost(s, "/v1/messages", claude)
	// an OpenAI client asks for a Claude model: translated, still the Anthropic endpoint
	keyxPost(s, "/v1/chat/completions", strings.Replace(claude, `"max_tokens":8,`, "", 1))
	// Claude Code asks for a GPT model: translated to the OpenAI endpoint
	keyxPost(s, "/v1/messages", gpt)

	keyxCheckHits(t, up.seen(),
		"/v1/messages ant-key",
		"/v1/messages ant-key",
		"/v1/chat/completions oai-key",
	)
}

func TestKeyxKeyWithoutEndpointDoesNotParticipate(t *testing.T) {
	up, srv := keyxServe(t, map[string]int{"plain": 400, "ant-key": 400})
	// no Anthropic endpoint at all, so the Anthropic-only key must be left out
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "plain", Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "ant", "ant-key", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	s := New()
	claude := `{"model":"relay/claude-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	keyxPost(s, "/v1/messages", claude)
	keyxCheckHits(t, up.seen(), "/v1/chat/completions plain")
}
