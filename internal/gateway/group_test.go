package gateway

import (
	"context"
	"encoding/json"
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

// rgxKeyLog is an upstream that answers every request, recording the key and the
// vendor's model id each one came with. Keys in limited answer 429.
type rgxKeyLog struct {
	mu      sync.Mutex
	limited map[string]bool
	keys    []string
	models  []string
}

func (k *rgxKeyLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var m struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(body, &m)
	k.mu.Lock()
	k.keys = append(k.keys, key)
	k.models = append(k.models, m.Model)
	limited := k.limited[key]
	k.mu.Unlock()
	if limited {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	if m.Stream {
		// a stream, as a translated request asks for: one Chat chunk, then done
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"from-`+key+`"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

// rgxKeysFor are the keys the requests for one vendor model went with, in order.
func (k *rgxKeyLog) rgxKeysFor(model string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for i, m := range k.models {
		if m == model {
			out = append(out, k.keys[i])
		}
	}
	return out
}

// rgxGrpSetup gives a test its own provider file and caches, and a fresh routing
// state; the returned upstream serves every provider the test saves.
func rgxGrpSetup(t *testing.T) (*rgxKeyLog, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
	affinities.Lock()
	affinities.m = map[string]*memory{}
	affinities.Unlock()
	routed.Lock()
	routed.turn = map[string]int{}
	routed.used = map[string]tokenUse{}
	routed.failures = map[string]int{}
	routed.Unlock()
	up := &rgxKeyLog{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return up, srv.URL + "/v1"
}

func rgxGrpSend(s *Server, path, body string, hdr map[string]string) (int, string) {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// rgxLastRoute is the most recent request's route in the trace.
func rgxLastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no route traced")
	}
	return rs[len(rs)-1]
}

// Chat messages for a conversation: a user turn, a tool call and its result
// within the turn, and an assistant reply.
const (
	rgxGrpUser     = `{"role":"user","content":"hi"}`
	rgxGrpToolCall = `{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}`
	rgxGrpToolRes  = `{"role":"tool","tool_call_id":"c1","content":"ok"}`
	rgxGrpReply    = `{"role":"assistant","content":"ok"}`
)

func rgxGrpBody(model string, msgs ...string) string {
	return `{"model":"` + model + `","messages":[` + strings.Join(msgs, ",") + `]}`
}

func rgxGrpUserText(s string) string { return `{"role":"user","content":"` + s + `"}` }

// A group of two providers, the first answering 429: the group's order sends
// the request to the first member's key, then the second answers.
func TestRgxGroupOrderTriesMembersInTurn(t *testing.T) {
	up, base := rgxGrpSetup(t)
	up.limited["kb"] = true
	for _, p := range []provider.Provider{
		{ID: "b", Name: "B", Chat: base, Key: "kb", Models: []string{"vendor/m"}},
		{ID: "a", Name: "A", Chat: base, Key: "ka", Models: []string{"only-a"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("group/mine", rgxGrpUser), nil)
	if code != 200 || !strings.Contains(body, "from-ka") {
		t.Fatalf("%d %s", code, body)
	}
	if !slices.Equal(up.keys, []string{"kb", "ka"}) || !slices.Equal(up.models, []string{"vendor/m", "only-a"}) {
		t.Fatalf("keys %v models %v", up.keys, up.models)
	}
	r := rgxLastRoute(t, s)
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Name != "Mine" || r.Group.Routing != provider.Ordered || r.Group.Auto {
		t.Fatalf("group %+v", r.Group)
	}
	if !slices.Equal(r.Group.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("order %+v", r.Order)
	}
}

// A group's rotation goes over its members' keys together: each new turn moves
// on, and the turn's requests stay on the key it began with.
func TestRgxGroupRotatesAcrossMembers(t *testing.T) {
	up, base := rgxGrpSetup(t)
	for _, p := range []provider.Provider{
		{ID: "ga", Name: "GA", Chat: base, Key: "ka"},
		{ID: "gb", Name: "GB", Chat: base, Key: "kb"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "pair", Members: []string{"ga/m", "gb/m"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "pair-1"}
	for i := range 2 {
		if code, body := rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("group/pair", rgxGrpUser), hdr); code != 200 {
			t.Fatalf("turn %d: %d %s", i, code, body)
		}
	}
	if !slices.Equal(up.keys, []string{"ka", "kb"}) {
		t.Fatalf("keys %v", up.keys)
	}
}

// Within a turn the tool results stay on the key the turn began with (default
// affinity), and the trace says why.
func TestRgxAffinityKeepsTurnWithinTurn(t *testing.T) {
	up, base := rgxGrpSetup(t)
	if err := provider.Save(provider.Provider{ID: "r", Name: "R", Chat: base, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("r", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "turn-1"}
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("r/m1", rgxGrpUser), hdr)
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("r/m1", rgxGrpUser, rgxGrpToolCall, rgxGrpToolRes), hdr)
	if !slices.Equal(up.keys, []string{"k1", "k1"}) {
		t.Fatalf("keys %v", up.keys)
	}
	a := rgxLastRoute(t, s).Affinity
	if a == nil || !a.Kept || !a.Within || a.Why != "turn" || a.Turn != 1 || a.Mode != "auto" || a.Last == "" {
		t.Fatalf("affinity %+v", a)
	}
}

// Session affinity keeps the whole conversation on one key, even across turns
// that rotation would move on.
func TestRgxAffinitySessionKeepsConversation(t *testing.T) {
	up, base := rgxGrpSetup(t)
	if err := provider.Save(provider.Provider{ID: "s", Name: "S", Chat: base, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("s", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("s", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "sess-1"}
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("s/m1", rgxGrpUser), hdr)
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("s/m1", rgxGrpUser, rgxGrpReply, rgxGrpUser), hdr)
	if !slices.Equal(up.keys, []string{"k1", "k1"}) {
		t.Fatalf("keys %v", up.keys)
	}
	if a := rgxLastRoute(t, s).Affinity; a == nil || a.Why != "session" || a.Within {
		t.Fatalf("affinity %+v", a)
	}
}

// Affinity off keeps nothing: the trace says so, and a new turn rotates.
func TestRgxAffinityOffKeepsNothing(t *testing.T) {
	up, base := rgxGrpSetup(t)
	if err := provider.Save(provider.Provider{ID: "o", Name: "O", Chat: base, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("o", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("o", provider.AffinityOff); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "off-1"}
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("o/m1", rgxGrpUser), hdr)
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("o/m1", rgxGrpUser, rgxGrpReply, rgxGrpUser), hdr)
	if !slices.Equal(up.keys, []string{"k1", "k2"}) {
		t.Fatalf("keys %v", up.keys)
	}
	if a := rgxLastRoute(t, s).Affinity; a == nil || a.Kept || a.Why != "off" {
		t.Fatalf("affinity %+v", a)
	}
}

// The last answerer resting moves the conversation to another key, and the
// trace says resting; a new session starts afresh.
func TestRgxAffinityRestingAndNewSession(t *testing.T) {
	up, base := rgxGrpSetup(t)
	if err := provider.Save(provider.Provider{ID: "w", Name: "W", Chat: base, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "rest-1"}
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("w/m1", rgxGrpUser), hdr)
	first := rgxLastRoute(t, s).Tries[0].ID
	restingUntil.Lock()
	restingUntil.m[first] = time.Now().Add(time.Hour)
	restingUntil.note[first] = Rest{Why: failRate, By: "retry-after"}
	restingUntil.Unlock()
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("w/m1", rgxGrpUser, rgxGrpReply, rgxGrpUser), hdr)
	if !slices.Equal(up.keys, []string{"k1", "k2"}) {
		t.Fatalf("keys %v", up.keys)
	}
	if a := rgxLastRoute(t, s).Affinity; a == nil || a.Why != "resting" || a.Kept {
		t.Fatalf("affinity %+v", a)
	}
	rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody("w/m1", rgxGrpUser), map[string]string{"x-session-id": "rest-2"})
	if a := rgxLastRoute(t, s).Affinity; a == nil || a.Why != "first" {
		t.Fatalf("affinity %+v", a)
	}
}

// Rotation by turn: k1→k2→k3→k1 across new turns, the requests within a turn
// and a request for another model in between don't move it.
func TestRgxRotateMovesByTurnOnly(t *testing.T) {
	up, base := rgxGrpSetup(t)
	if err := provider.Save(provider.Provider{ID: "t", Name: "T", Chat: base, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("t", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	s := New()
	hdr := map[string]string{"x-session-id": "rot-1"}
	send := func(model string, msgs ...string) {
		t.Helper()
		if code, body := rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody(model, msgs...), hdr); code != 200 {
			t.Fatalf("%s: %d %s", model, code, body)
		}
	}
	history := []string{rgxGrpUser}
	send("t/m1", history...)
	history = append(history, rgxGrpToolCall, rgxGrpToolRes)
	send("t/m1", history...)
	send("t/other", rgxGrpUserText("side question"))
	history = append(history, rgxGrpReply, rgxGrpUserText("two"))
	send("t/m1", history...)
	history = append(history, rgxGrpReply, rgxGrpUserText("three"))
	send("t/m1", history...)
	history = append(history, rgxGrpReply, rgxGrpUserText("four"))
	send("t/m1", history...)
	if got := up.rgxKeysFor("m1"); !slices.Equal(got, []string{"k1", "k1", "k2", "k3", "k1"}) {
		t.Fatalf("keys for m1 %v (all %v)", got, up.keys)
	}
	if got := up.rgxKeysFor("other"); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("keys for other %v", got)
	}
}

// A group whose members are all unusable answers 404 saying so.
func TestRgxGroupWithNoMemberReadyIs404(t *testing.T) {
	rgxGrpSetup(t)
	if err := provider.SaveGroup(provider.Group{ID: "lost", Members: []string{"nowhere/x"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, id := range []string{"group/lost", "group/unknown"} {
		code, body := rgxGrpSend(s, "/v1/chat/completions", rgxGrpBody(id, rgxGrpUser), nil)
		if code != 404 || !strings.Contains(body, "没有模型就绪") {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
}

// A capability question about a group is answered by its first member.
func TestRgxGroupCapabilityIsItsFirstMember(t *testing.T) {
	_, base := rgxGrpSetup(t)
	for _, p := range []provider.Provider{
		{ID: "cb", Name: "CB", Chat: base, Key: "kb"},
		{ID: "ca", Name: "CA", Chat: base, Key: "ka"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "caps", Name: "Caps", Members: []string{"cb/x", "ca/x"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	// the id's slash travels escaped, as one path segment
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/group%2Fcaps", nil))
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	if rec.Code != 200 || obj["id"] != "group/caps" || obj["owned_by"] != "cb" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// A provider's key made for another protocol is tried only after the keys
// that suit the request: the trace marks it aside, and it answers when the
// others can't.
func TestRgxAsideKeyAnswersWhenPoolFails(t *testing.T) {
	up, base := rgxGrpSetup(t)
	up.limited["k1"], up.limited["k2"] = true, true
	// the unspecified keys speak Anthropic natively; kc is a Chat key, so an
	// Anthropic request reaches it translated
	if err := provider.Save(provider.Provider{ID: "pool", Name: "Pool", Chat: base, Anthropic: strings.TrimSuffix(base, "/v1"), Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "kc", Protocol: provider.Chat}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("pool", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	s := New()
	anthropic := `{"model":"pool/m1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	code, body := rgxGrpSend(s, "/v1/messages", anthropic, nil)
	if code != 200 || !strings.Contains(body, "from-kc") {
		t.Fatalf("%d %s", code, body)
	}
	if !slices.Equal(up.keys, []string{"k1", "k2", "kc"}) {
		t.Fatalf("keys %v", up.keys)
	}
	r := rgxLastRoute(t, s)
	if len(r.Order) != 3 || r.Order[0].Aside || r.Order[1].Aside || !r.Order[2].Aside {
		t.Fatalf("order %+v", r.Order)
	}
}
