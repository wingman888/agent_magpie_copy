package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fbkReply is what the fake upstream answers for one key.
type fbkReply struct {
	status int
	body   string
}

// fbkUp answers by the key it was called with and records "key model" for
// every call, so a test can see which provider and which model was used.
type fbkUp struct {
	mu    sync.Mutex
	reply map[string]fbkReply
	seen  []string
}

func (u *fbkUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw, _ := io.ReadAll(r.Body)
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(raw, &v)
	u.mu.Lock()
	u.seen = append(u.seen, key+" "+v.Model)
	rep, ok := u.reply[key]
	u.mu.Unlock()
	if !ok {
		rep = fbkReply{status: 500, body: `{"error":"no reply for this key"}`}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	io.WriteString(w, rep.body)
}

func (u *fbkUp) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

const fbkChatOK = `{"id":"c1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

// fbkEnv isolates config and starts a fake upstream that answers by key.
func fbkEnv(t *testing.T, reply map[string]fbkReply) (*fbkUp, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	u := &fbkUp{reply: reply}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	return u, up
}

// fbkPair stores a primary provider "fbk-p" that falls back to "fbk-f/m2".
func fbkPair(t *testing.T, up *httptest.Server, primary string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fbk-p", Name: "fbk-p", Chat: up.URL + "/v1",
		Key: primary, Fallback: []string{"fbk-f/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fbk-f", Name: "fbk-f", Chat: up.URL + "/v1",
		Key: "fbk-f-key"}); err != nil {
		t.Fatal(err)
	}
}

const fbkBody = `{"model":"fbk-p/m1","messages":[{"role":"user","content":"hi"}]}`

func fbkPost(h http.Handler, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestFbkFallsBackOnSpentStatuses(t *testing.T) {
	cases := map[string]fbkReply{
		"429":          {429, `{"error":"rate limited"}`},
		"402":          {402, `{"error":"payment required"}`},
		"529":          {529, `{"error":"overloaded"}`},
		"502":          {502, `{"error":"bad gateway"}`},
		"503":          {503, `{"error":"unavailable"}`},
		"504":          {504, `{"error":"gateway timeout"}`},
		"403 quota":    {403, `{"error":"您的额度已用完"}`},
		"403 quota en": {403, `{"error":{"message":"quota exceeded"}}`},
	}
	for name, rep := range cases {
		t.Run(name, func(t *testing.T) {
			u, up := fbkEnv(t, map[string]fbkReply{
				"fbk-p-key": rep,
				"fbk-f-key": {200, fbkChatOK},
			})
			fbkPair(t, up, "fbk-p-key")
			gw := New()
			code, body := fbkPost(gw.Handler(), fbkBody)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			got := u.calls()
			if len(got) != 2 || got[0] != "fbk-p-key m1" || got[1] != "fbk-f-key m2" {
				t.Fatalf("upstream calls = %v, want the primary then the fallback under its own model", got)
			}
			c := gw.Recent()[0]
			if c.Provider != "fbk-f" || c.Status != 200 {
				t.Fatalf("call = provider %q status %d, want fbk-f 200", c.Provider, c.Status)
			}
			if !strings.Contains(c.Fallback, "fbk-p") {
				t.Fatalf("fallback = %q, want the failed provider named", c.Fallback)
			}
		})
	}
}

func TestFbkOrdinaryErrorNotFallenBack(t *testing.T) {
	u, up := fbkEnv(t, map[string]fbkReply{
		"fbk-p-key": {400, `{"error":{"message":"bad request"}}`},
		"fbk-f-key": {200, fbkChatOK},
	})
	fbkPair(t, up, "fbk-p-key")
	gw := New()
	code, body := fbkPost(gw.Handler(), fbkBody)
	if code != 400 {
		t.Fatalf("status %d (%s), want the 400 passed through", code, body)
	}
	if got := u.calls(); len(got) != 1 {
		t.Fatalf("a 400 went on to the fallback: %v", got)
	}
	if f := gw.Recent()[0].Fallback; f != "" {
		t.Fatalf("fallback = %q for a 400", f)
	}
}

func TestFbkUnreachablePrimaryFallsBack(t *testing.T) {
	u, up := fbkEnv(t, map[string]fbkReply{"fbk-f-key": {200, fbkChatOK}})
	if err := provider.Save(provider.Provider{ID: "fbk-p", Name: "fbk-p", Chat: "http://127.0.0.1:1/v1",
		Key: "fbk-p-key", Fallback: []string{"fbk-f/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fbk-f", Name: "fbk-f", Chat: up.URL + "/v1", Key: "fbk-f-key"}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	if code, body := fbkPost(gw.Handler(), fbkBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.calls(); len(got) != 1 || got[0] != "fbk-f-key m2" {
		t.Fatalf("upstream calls = %v", got)
	}
}

func TestFbkLastCandidateErrorReturned(t *testing.T) {
	_, up := fbkEnv(t, map[string]fbkReply{
		"fbk-p-key": {429, `{"error":"rate limited"}`},
		"fbk-f-key": {503, `{"error":"fallback is down"}`},
	})
	fbkPair(t, up, "fbk-p-key")
	gw := New()
	code, body := fbkPost(gw.Handler(), fbkBody)
	if code != 503 {
		t.Fatalf("status %d (%s), want the last candidate's 503", code, body)
	}
	if !strings.Contains(body, "fbk-f") {
		t.Fatalf("body %q does not carry the last candidate's error", body)
	}
}

func TestFbkFailedProviderRestsForAMinute(t *testing.T) {
	u, up := fbkEnv(t, map[string]fbkReply{
		"fbk-p-key": {429, `{"error":"rate limited"}`},
		"fbk-f-key": {200, fbkChatOK},
	})
	fbkPair(t, up, "fbk-p-key")
	gw := New()
	now := time.Now()
	gw.now = func() time.Time { return now }
	h := gw.Handler()

	if code, body := fbkPost(h, fbkBody); code != 200 {
		t.Fatalf("first request: status %d: %s", code, body)
	}
	// within the minute the fallback is tried first, so the primary is not called
	if code, body := fbkPost(h, fbkBody); code != 200 {
		t.Fatalf("second request: status %d: %s", code, body)
	}
	got := u.calls()
	if len(got) != 3 || got[2] != "fbk-f-key m2" {
		t.Fatalf("second request went to the resting primary: %v", got)
	}
	if f := gw.Recent()[0].Fallback; f != "" {
		t.Fatalf("second request fell back again: %q", f)
	}

	// after the minute the primary is tried again
	now = now.Add(2 * time.Minute)
	fbkPost(h, fbkBody)
	got = u.calls()
	if len(got) != 5 || got[3] != "fbk-p-key m1" {
		t.Fatalf("primary was not retried after the rest: %v", got)
	}
}

func TestFbkUnparseableEntriesSkipped(t *testing.T) {
	u, up := fbkEnv(t, map[string]fbkReply{
		"fbk-p-key": {429, `{"error":"rate limited"}`},
		"fbk-f-key": {200, fbkChatOK},
	})
	if err := provider.Save(provider.Provider{ID: "fbk-p", Name: "fbk-p", Chat: up.URL + "/v1",
		Key: "fbk-p-key", Fallback: []string{"no-slash-here", "fbk-nobody/m9", "fbk-f/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fbk-f", Name: "fbk-f", Chat: up.URL + "/v1", Key: "fbk-f-key"}); err != nil {
		t.Fatal(err)
	}
	gw := New()
	if code, body := fbkPost(gw.Handler(), fbkBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.calls(); len(got) != 2 || got[1] != "fbk-f-key m2" {
		t.Fatalf("upstream calls = %v", got)
	}
}
