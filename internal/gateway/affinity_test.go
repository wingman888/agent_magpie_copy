package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyed is an upstream that records the key each request arrived with, and
// answers a key's requests with the status set for it, 200 otherwise.
type keyed struct {
	mu   sync.Mutex
	seen []string
	fail map[string]int
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	code := k.fail[key]
	k.mu.Unlock()
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, groupChatReply)
}

// take is the keys the upstream saw since the last take.
func (k *keyed) take() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := k.seen
	k.seen = nil
	return out
}

func (k *keyed) setFail(key string, code int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail == nil {
		k.fail = map[string]int{}
	}
	k.fail[key] = code
}

// isolate gives the test its own config and cache, and the gateway it asks.
func isolate(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return New()
}

// keyedProvider saves a Chat provider whose first key is keys[0], with the
// rest as further keys, on the upstream.
func keyedProvider(t *testing.T, id string, up *httptest.Server, routing, affinity string, keys ...string) {
	t.Helper()
	p := provider.Provider{ID: id, Name: id, Chat: up.URL + "/v1", Key: keys[0], Models: []string{"m"},
		Routing: routing, Affinity: affinity}
	for _, k := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// ask sends one request for model in a session and returns the route the
// trace kept for it; the request must have been answered.
func ask(t *testing.T, s *Server, model, session string, msgs string) Route {
	t.Helper()
	body := `{"model":"` + model + `","messages":[` + msgs + `]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	st := s.Trace(context.Background(), 0, 0)
	return st.Routes[len(st.Routes)-1]
}

const (
	userMsg  = `{"role":"user","content":"hi"}`
	toolTurn = `{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"ok"}`
	nextMsg  = `{"role":"assistant","content":"done"},{"role":"user","content":"more"}`
)

// keyOf is the key the upstream saw the last of want requests arrive with.
func keyOf(t *testing.T, up *keyed, want int) string {
	t.Helper()
	got := up.take()
	if len(got) != want {
		t.Fatalf("upstream saw %v, want %d requests", got, want)
	}
	return got[len(got)-1]
}

// Default affinity: a tool-result request in the same turn stays on the key
// that answered the turn's first request, whatever routing says.
func TestAffinityAutoKeepsTheTurn(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "aff-auto", srv, provider.Rotate, "", "a1", "a2", "a3")

	ask(t, s, "aff-auto/m", "aff-auto-s", userMsg)
	first := keyOf(t, up, 1)
	r := ask(t, s, "aff-auto/m", "aff-auto-s", userMsg+","+toolTurn)
	if got := keyOf(t, up, 1); got != first {
		t.Fatalf("tool result went to %s, the turn began on %s", got, first)
	}
	if r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "turn" || !r.Affinity.Within {
		t.Fatalf("affinity %+v", r.Affinity)
	}
}

// session mode keeps the whole conversation on one key, across turns, even
// where rotation would move it on.
func TestAffinitySessionKeepsTheConversation(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "aff-sess", srv, provider.Rotate, "session", "s1", "s2", "s3")

	ask(t, s, "aff-sess/m", "aff-sess-s", userMsg)
	first := keyOf(t, up, 1)
	r := ask(t, s, "aff-sess/m", "aff-sess-s", userMsg+","+nextMsg)
	if got := keyOf(t, up, 1); got != first {
		t.Fatalf("second turn went to %s, the first to %s", got, first)
	}
	if r.Affinity == nil || r.Affinity.Why != "session" || !r.Affinity.Kept {
		t.Fatalf("affinity %+v", r.Affinity)
	}
	ask(t, s, "aff-sess/m", "aff-sess-s", userMsg+","+nextMsg+","+nextMsg)
	if got := keyOf(t, up, 1); got != first {
		t.Fatalf("third turn went to %s, the first to %s", got, first)
	}
}

// off keeps nothing: rotation moves on at each new turn.
func TestAffinityOffMovesOn(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "aff-off", srv, provider.Rotate, "off", "o1", "o2", "o3")

	ask(t, s, "aff-off/m", "aff-off-s", userMsg)
	first := keyOf(t, up, 1)
	r := ask(t, s, "aff-off/m", "aff-off-s", userMsg+","+nextMsg)
	if got := keyOf(t, up, 1); got == first {
		t.Fatalf("off kept %s", got)
	}
	if r.Affinity == nil || r.Affinity.Why != "off" || r.Affinity.Kept {
		t.Fatalf("affinity %+v", r.Affinity)
	}
}

// A new session id is a new conversation: nothing to keep, why "first".
func TestAffinityNewSessionIsFirst(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "aff-first", srv, provider.Rotate, "", "f1", "f2")

	ask(t, s, "aff-first/m", "aff-first-a", userMsg)
	keyOf(t, up, 1)
	r := ask(t, s, "aff-first/m", "aff-first-b", userMsg+","+toolTurn)
	keyOf(t, up, 1)
	if r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("affinity %+v", r.Affinity)
	}
}

// The last answerer resting sends the conversation elsewhere, why "resting".
func TestAffinityRestingAnswererIsLeftBehind(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "aff-rest", srv, "", "", "r1", "r2")

	ask(t, s, "aff-rest/m", "aff-rest-s", userMsg)
	first := keyOf(t, up, 1)
	// the last answerer is put out of action, as a failure would
	rest := "aff-rest#" + provider.KeyID(first)
	restingUntil.Lock()
	restingUntil.m[rest] = time.Now().Add(time.Hour)
	restingUntil.note[rest] = Rest{Why: failRate, Status: 429, Until: time.Now().Add(time.Hour), By: "cooldown"}
	restingUntil.Unlock()
	t.Cleanup(func() {
		restingUntil.Lock()
		delete(restingUntil.m, rest)
		delete(restingUntil.note, rest)
		restingUntil.Unlock()
	})
	r := ask(t, s, "aff-rest/m", "aff-rest-s", userMsg+","+toolTurn)
	if got := keyOf(t, up, 1); got == first {
		t.Fatalf("resting %s was tried again", first)
	}
	if r.Affinity == nil || r.Affinity.Why != "resting" {
		t.Fatalf("affinity %+v", r.Affinity)
	}
}

// Rotation moves a conversation's key on per user turn: k1, k2, k3, then k1
// again, and a tool-result request, or another model asked in between, does
// not move it along.
func TestRotateMovesPerTurn(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "rot3", srv, provider.Rotate, "", "k1", "k2", "k3")
	keyedProvider(t, "side", srv, "", "", "side-key")

	var turns []string
	msgs := []string{userMsg}
	for turn := 0; turn < 4; turn++ {
		ask(t, s, "rot3/m", "rot-s", strings.Join(msgs, ","))
		key := keyOf(t, up, 1)
		turns = append(turns, key)
		ask(t, s, "rot3/m", "rot-s", strings.Join(append(msgs, toolTurn), ","))
		if got := keyOf(t, up, 1); got != key {
			t.Fatalf("turn %d: tool result went to %s, not %s", turn, got, key)
		}
		ask(t, s, "side/m", "rot-s", userMsg) // another model, same session
		up.take()
		msgs = append(msgs, nextMsg)
	}
	if got := strings.Join(turns, " "); got != "k1 k2 k3 k1" {
		t.Fatalf("turns went to %q", got)
	}
}

// A routing group tries its members in order: the first member's key is
// rate limited, so the second member answers, and the trace names the group.
func TestGroupOrderFailsOverToNextMember(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "gor-a", srv, "", "", "ga")
	keyedProvider(t, "gor-b", srv, "", "", "gb")
	up.setFail("gb", 429)
	if err := provider.SaveGroup(provider.Group{ID: "gor", Members: []string{"gor-b/m", "gor-a/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	r := ask(t, s, "group/gor", "gor-s", userMsg)
	if got := up.take(); !slices.Equal(got, []string{"gb", "ga"}) {
		t.Fatalf("keys tried %v", got)
	}
	if r.Group == nil || r.Group.ID != "gor" {
		t.Fatalf("group %+v", r.Group)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "gor-b" || r.Order[1].Provider != "gor-a" {
		t.Fatalf("order %+v", r.Order)
	}
}

// Within a group, a rotation covers the keys of every member together, and
// a key made for another protocol is an aside.
func TestGroupRotatesAcrossMembers(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "grt-a", srv, "", "", "ta")
	keyedProvider(t, "grt-b", srv, "", "", "tb")
	if err := provider.SaveGroup(provider.Group{ID: "grt", Members: []string{"grt-a/m", "grt-b/m"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	msgs := []string{userMsg}
	for range 3 {
		ask(t, s, "group/grt", "grt-s", strings.Join(msgs, ","))
		keys = append(keys, keyOf(t, up, 1))
		msgs = append(msgs, nextMsg)
	}
	if got := strings.Join(keys, " "); got != "ta tb ta" {
		t.Fatalf("group rotation went to %q", got)
	}
}
