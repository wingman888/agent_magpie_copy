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

// s2Env keeps config, cache and home out of the real ones.
func s2Env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// s2Upstream records which key and path each request came with. Keys listed
// in fail get that status back; count_tokens gets a count.
type s2Upstream struct {
	mu    sync.Mutex
	keys  []string
	paths []string
	fail  map[string]int
}

func (u *s2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.keys = append(u.keys, key)
	u.paths = append(u.paths, r.URL.Path)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code := u.fail[key]; code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"you exceeded your current quota"}}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "count_tokens") {
		io.WriteString(w, `{"input_tokens":7}`)
		return
	}
	io.WriteString(w, `{"id":"s2","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *s2Upstream) seen() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.keys, ",")
}

// s2Post sends one request through the gateway's own handler.
func s2Post(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const s2Chat = `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`

func s2Provider(t *testing.T, up *httptest.Server, key string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Key: key, KeyName: "home", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestS2KeyFailoverServesFromSecondKey(t *testing.T) {
	s2Env(t)
	up := &s2Upstream{fail: map[string]int{"sk-s2-one": 429}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	s2Provider(t, srv, "sk-s2-one")
	if err := provider.AddKey("fake", "team", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := s2Post(t, s, "/v1/chat/completions", s2Chat); code != 200 || !strings.Contains(body, `"hi"`) {
		t.Fatalf("first request: %d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "fake" || !strings.Contains(c.Fallback, "fake") || !strings.Contains(c.Fallback, "home") || c.Key != "team" {
		t.Fatalf("call record = provider %q fallback %q key %q", c.Provider, c.Fallback, c.Key)
	}
	if code, _ := s2Post(t, s, "/v1/chat/completions", s2Chat); code != 200 {
		t.Fatalf("second request: %d", code)
	}
	if got := up.seen(); got != "sk-s2-one,sk-s2-two,sk-s2-two" {
		t.Fatalf("keys tried = %s, want the failed one once, then the second key straight away", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Key != "team" {
		t.Fatalf("second request: fallback %q key %q, want none and team", c.Fallback, c.Key)
	}
}

func TestS2BadRequestIsNotRetriedWithAnotherKey(t *testing.T) {
	s2Env(t)
	up := &s2Upstream{fail: map[string]int{"sk-s2-one": 400}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	s2Provider(t, srv, "sk-s2-one")
	if err := provider.AddKey("fake", "team", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, _ := s2Post(t, s, "/v1/chat/completions", s2Chat); code != 400 {
		t.Fatalf("status = %d, want the 400 passed through", code)
	}
	if got := up.seen(); got != "sk-s2-one" {
		t.Fatalf("keys tried = %s, want only the first", got)
	}
}

func TestS2LastKeyErrorReachesClient(t *testing.T) {
	s2Env(t)
	up := &s2Upstream{fail: map[string]int{"sk-s2-one": 429, "sk-s2-two": 429}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	s2Provider(t, srv, "sk-s2-one")
	if err := provider.AddKey("fake", "team", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := s2Post(t, s, "/v1/chat/completions", s2Chat)
	if code != 429 || !strings.Contains(body, "quota") {
		t.Fatalf("status %d body %s, want the last key's 429", code, body)
	}
	if c := s.Recent()[0]; c.Fallback == "" {
		t.Fatal("exhausted keys left no fallback record")
	}
}

func TestS2SwitchedOffKeyIsNeverTried(t *testing.T) {
	s2Env(t)
	up := &s2Upstream{fail: map[string]int{"sk-s2-one": 429}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	s2Provider(t, srv, "sk-s2-one")
	if err := provider.AddKey("fake", "team", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("fake", provider.KeyRef("sk-s2-two"), false); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, _ := s2Post(t, s, "/v1/chat/completions", s2Chat); code != 429 {
		t.Fatalf("status = %d, want 429 with no other key on", code)
	}
	if got := up.seen(); got != "sk-s2-one" {
		t.Fatalf("keys tried = %s; the switched-off key was used", got)
	}
}

func TestS2CountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	s2Env(t)
	up := &s2Upstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "sk-s2-oai", KeyProtocol: provider.Chat, Models: []string{"claude-x"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := s2Post(t, s, "/v1/messages/count_tokens", `{"model":"fake/claude-x","messages":[{"role":"user","content":"hello"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("status %d body %s", code, body)
	}
	if got := up.seen(); got != "" {
		t.Fatalf("an OpenAI-only key was used for count_tokens: %s", got)
	}
	if err := provider.AddKey("fake", "ant", "sk-s2-ant", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	if code, _ := s2Post(t, s, "/v1/messages/count_tokens", `{"model":"fake/claude-x","messages":[{"role":"user","content":"hello"}]}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := up.seen(); got != "sk-s2-ant" || up.paths[0] != "/v1/messages/count_tokens" {
		t.Fatalf("keys = %s paths = %v; want the Anthropic key on count_tokens", got, up.paths)
	}
}
