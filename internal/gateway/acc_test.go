package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// rggAccBackend is a Chat backend that records the key each call came with. A
// key in fail answers 429; the others answer 200.
type rggAccBackend struct {
	mu   sync.Mutex
	keys []string
	fail map[string]bool
}

func (b *rggAccBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.keys = append(b.keys, key)
	fails := b.fail[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fails {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	}
	io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}

// took is the keys the backend was called with since it was last asked.
func (b *rggAccBackend) took() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.keys
	b.keys = nil
	return k
}

// Request bodies: rggAccFirst opens a turn; rggAccTool is the turn's tool result
// sent back; rggAccNext opens the turn after one whose tool result came back.
const (
	rggAccFirst = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`
	rggAccTool  = `{"model":"%s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`
	rggAccNext  = `{"model":"%s","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`
)

// rggAccPost sends one chat request in session sid ("" for none) and returns
// its status.
func rggAccPost(t *testing.T, base, body, sid string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sid != "" {
		req.Header.Set("x-session-id", sid)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Logf("status %d: %s", res.StatusCode, b)
	}
	return res.StatusCode
}

// rggAccWhys is why each routed request with an affinity went as it did, in
// order.
func rggAccWhys(s *Server) []string {
	var out []string
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Affinity != nil {
			out = append(out, r.Affinity.Why)
		}
	}
	return out
}

func rggAccRest(id string) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
}

func rggAccUnrest(id string) {
	restingUntil.Lock()
	delete(restingUntil.m, id)
	restingUntil.Unlock()
}

func rggAccKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keys %v, want %v", got, want)
	}
}

// Acceptance A2: a group saved as "Mine" has the id mine; in order it tries
// its first member, a 429 hands the request to the second, and the trace
// names the group with its members and candidates in saved order.
func TestRgAccA2MineInOrder(t *testing.T) {
	rggResetGroupTest(t)
	busy := &rggAccBackend{fail: map[string]bool{"kb": true}}
	bs := httptest.NewServer(busy)
	defer bs.Close()
	spare := &rggAccBackend{}
	ss := httptest.NewServer(spare)
	defer ss.Close()
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"vendor/m"}, Chat: bs.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"only-a"}, Chat: ss.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if code := rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "group/mine"), ""); code != 200 {
		t.Fatalf("group/mine answered %d", code)
	}
	rggAccKeys(t, busy.took(), "kb")
	rggAccKeys(t, spare.took(), "ka")
	var rt *Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		if r.Group != nil {
			x := r
			rt = &x
		}
	}
	if rt == nil {
		t.Fatal("no trace of group/mine")
	}
	if rt.Group.ID != "mine" || rt.Group.Routing != "order" || strings.Join(rt.Group.Members, " ") != "b/vendor/m a/only-a" {
		t.Errorf("group trace %+v", rt.Group)
	}
	if len(rt.Order) != 2 || rt.Order[0].Provider != "b" || rt.Order[1].Provider != "a" {
		t.Errorf("candidate order %+v, want b then a", rt.Order)
	}
}

// Acceptance A5: within a turn, the second request (tool result sent back)
// stays on the key that answered the first — unless the mode says otherwise —
// and the trace says why. Here k1 rests for the first request, so k2 answers
// it, and k1 is then free again.
func TestRgAccA5AffinityWithinTurn(t *testing.T) {
	cases := []struct {
		mode   string
		whys   []string
		second string // the key of the tool-result request
		third  string // the key of the next turn's request
	}{
		{"", []string{"first", "turn", "no-cache"}, "k2", "k1"},
		{"session", []string{"first", "session", "session"}, "k2", "k2"},
		{"turn", []string{"first", "turn", "new-turn"}, "k2", "k1"},
		{"off", []string{"off", "off", "off"}, "k1", "k1"},
	}
	for _, c := range cases {
		name := c.mode
		if name == "" {
			name = "auto"
		}
		t.Run(name, func(t *testing.T) {
			rggResetGroupTest(t)
			b := &rggAccBackend{}
			srv := httptest.NewServer(b)
			defer srv.Close()
			if err := provider.Save(provider.Provider{ID: "acc", Name: "Acc", Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}},
				Chat: srv.URL + "/v1", Models: []string{"m"}}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetAffinity("acc", c.mode); err != nil {
				t.Fatal(err)
			}
			k1 := "acc#" + provider.KeyID("k1")
			rggAccRest(k1)
			s := New()
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()
			sid := "acc-a5-" + name
			if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "m"), sid) != 200 {
				t.Fatal("first request failed")
			}
			rggAccKeys(t, b.took(), "k2")
			rggAccUnrest(k1)
			if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccTool, "m"), sid) != 200 {
				t.Fatal("tool-result request failed")
			}
			rggAccKeys(t, b.took(), c.second)
			if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccNext, "m"), sid) != 200 {
				t.Fatal("next-turn request failed")
			}
			rggAccKeys(t, b.took(), c.third)
			if got := rggAccWhys(s); strings.Join(got, " ") != strings.Join(c.whys, " ") {
				t.Errorf("why %v, want %v", got, c.whys)
			}
		})
	}
}

// Acceptance A6: under rotate, each new turn moves to the next key and the
// turn's tool-result requests stay where it started. Another model's request
// in between doesn't move the turns.
func TestRgAccA6RotateStepsPerTurn(t *testing.T) {
	rggResetGroupTest(t)
	b := &rggAccBackend{}
	srv := httptest.NewServer(b)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "accrot", Name: "Rot", Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}},
		Routing: provider.Rotate, Chat: srv.URL + "/v1", Models: []string{"m", "small"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	const sid = "accrot-session"
	steps := []struct{ body, model, want string }{
		{rggAccFirst, "m", "k1"},
		{rggAccTool, "m", "k1"},
		{rggAccFirst, "small", "k1"}, // another model in between
		{rggAccNext, "m", "k2"},
		{rggAccTool, "m", "k2"},
		{rggAccNext, "m", "k3"},
		{rggAccFirst, "m", "k1"},
	}
	for i, st := range steps {
		if code := rggAccPost(t, ts.URL, fmt.Sprintf(st.body, st.model), sid); code != 200 {
			t.Fatalf("step %d answered %d", i, code)
		}
		rggAccKeys(t, b.took(), st.want)
	}
}

// Acceptance A7: a conversation whose last answerer is resting moves to
// another key, with why "resting"; a different session id starts again with
// "first".
func TestRgAccA7RestingAnswererAndNewSession(t *testing.T) {
	rggResetGroupTest(t)
	b := &rggAccBackend{}
	srv := httptest.NewServer(b)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "acc7", Name: "Acc7", Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}},
		Chat: srv.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	k1 := "acc7#" + provider.KeyID("k1")
	k2 := "acc7#" + provider.KeyID("k2")
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	rggAccRest(k1)
	if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "m"), "acc7-s") != 200 {
		t.Fatal("first request failed")
	}
	rggAccKeys(t, b.took(), "k2")
	rggAccUnrest(k1)
	rggAccRest(k2)
	if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccTool, "m"), "acc7-s") != 200 {
		t.Fatal("tool-result request failed")
	}
	rggAccKeys(t, b.took(), "k1")
	rggAccUnrest(k2)
	if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "m"), "acc7-other") != 200 {
		t.Fatal("other session failed")
	}
	rggAccKeys(t, b.took(), "k1")
	if got := rggAccWhys(s); strings.Join(got, " ") != "first resting first" {
		t.Errorf("why %v, want first resting first", got)
	}
}

// Acceptance A9: with rotate, a Chat request's main pool is the key made for
// Chat; the keys that name no protocol come after it, as asides, and answer
// when the main pool fails.
func TestRgAccA9AsidesAfterMainPool(t *testing.T) {
	rggResetGroupTest(t)
	b := &rggAccBackend{}
	srv := httptest.NewServer(b)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "accmix", Name: "Mix", Key: "k1",
		Keys:    []provider.KeyAccount{{Key: "k2"}, {Key: "kc", Protocol: provider.Chat}},
		Routing: provider.Rotate, Chat: srv.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "m"), "") != 200 {
		t.Fatal("request failed")
	}
	rggAccKeys(t, b.took(), "kc")
	var rt Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		rt = r
	}
	if len(rt.Order) != 3 || rt.Order[0].Aside || !rt.Order[1].Aside || !rt.Order[2].Aside {
		t.Fatalf("order %+v: want the Chat key first, the others aside", rt.Order)
	}
	if rt.Order[0].ID != "accmix#"+provider.KeyID("kc") {
		t.Errorf("main pool is %s, want the Chat key", rt.Order[0].ID)
	}

	// the main pool fails: an aside answers
	b.fail = map[string]bool{"kc": true}
	if rggAccPost(t, ts.URL, fmt.Sprintf(rggAccFirst, "m"), "") != 200 {
		t.Fatal("the aside did not answer when the main pool failed")
	}
	got := b.took()
	if len(got) != 2 || got[0] != "kc" || (got[1] != "k1" && got[1] != "k2") {
		t.Errorf("keys %v, want kc then an aside", got)
	}
}

// Session ids come from the first of the headers that is set, in order.
func TestRgAccSessionHeaderOrder(t *testing.T) {
	h := http.Header{}
	h.Set("x-claude-code-session-id", "f")
	h.Set("session-id", "e")
	if got := sessionID(h); got != "e" {
		t.Errorf("session-id: %q, want e", got)
	}
	h.Set("session_id", "d")
	if got := sessionID(h); got != "d" {
		t.Errorf("session_id: %q, want d", got)
	}
	h.Set("x-session-id", "c")
	h.Set("x-session-affinity", "b")
	if got := sessionID(h); got != "b" {
		t.Errorf("x-session-affinity before x-session-id: %q, want b", got)
	}
	h.Del("x-session-affinity")
	if got := sessionID(h); got != "c" {
		t.Errorf("x-session-id: %q, want c", got)
	}
	h.Set("x-opencode-session", "a")
	if got := sessionID(h); got != "a" {
		t.Errorf("x-opencode-session: %q, want a", got)
	}
}
