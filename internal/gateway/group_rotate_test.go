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

// warmLog is an upstream answering Chat requests with 2000 cache tokens read,
// so a conversation's affinity would keep its last key were it allowed to. It
// keeps the Authorization header of each request, in order.
type warmLog struct {
	mu   sync.Mutex
	auth []string
}

const warmOK = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3000,"completion_tokens":1,"total_tokens":3001,"prompt_tokens_details":{"cached_tokens":2000}}}`

func (l *warmLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	l.mu.Lock()
	l.auth = append(l.auth, r.Header.Get("Authorization"))
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, warmOK)
}

// TestGroupRotatingMemberMovesOnEachTurn: an order group whose member rotates
// its keys goes to the next key at each new user turn, even when the cache is
// warm; within a turn the tool-result requests stay on the turn's key.
func TestGroupRotatingMemberMovesOnEachTurn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &warmLog{}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	p := provider.Provider{ID: "gr-rot", Name: "R", Key: "sk-k1", Keys: []provider.KeyAccount{{Key: "sk-k2"}, {Key: "sk-k3"}},
		Models: []string{"m"}, Routing: provider.Rotate, Chat: ts.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "grp", Name: "G", Routing: provider.Ordered,
		Members: []string{"gr-rot/m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()

	// send makes one request of the group in session, and returns the key that
	// answered it and the route it was traced with.
	send := func(msgs, want string) Route {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/grp","messages":`+msgs+`}`))
		req.Header.Set("x-session-id", "grp-rot-1")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		up.mu.Lock()
		auth := up.auth
		up.auth = nil
		up.mu.Unlock()
		if len(auth) != 1 {
			t.Fatalf("%d upstream requests, want 1", len(auth))
		}
		key := ""
		for _, k := range []string{"k1", "k2", "k3"} {
			if strings.Contains(auth[0], "sk-"+k) {
				key = k
			}
		}
		if key != want {
			t.Fatalf("answered by %q (%s), want %q", key, auth[0], want)
		}
		return lastRoute(t, s)
	}

	send(msgFirst, "k1")
	if r := send(msgTool, "k1"); why(r) != "turn" {
		t.Fatalf("within turn: why %q", why(r))
	}
	if r := send(msgNext, "k2"); why(r) != "new-turn" {
		t.Fatalf("new turn: why %q, want new-turn", why(r))
	}
	send(msgTool, "k2")
	send(msgNext, "k3")
	send(msgNext, "k1")
}
