package gateway

// Black-box checks of acceptance items 2, 3, 5, 6, 7 and 9, and of the
// routing trace's field names: an httptest upstream that answers by key,
// magpie's own HTTP handler, and the exported provider entry points.

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// accProvider_f09 saves a provider with the given keys (the first is its
// primary) and lists the models at a fake upstream that answers by key.
// Models may be nil: then the provider serves any model asked of it.
func accProvider_f09(t *testing.T, id, routing string, models []string, keys ...string) *grpUpstream_f09 {
	t.Helper()
	up := &grpUpstream_f09{keys: keys, code: map[string]int{}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	chat := srv.URL + "/v1"
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: keys[0], Chat: chat, Routing: routing}); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys[1:] {
		if err := provider.AddKey(id, "", k, ""); err != nil {
			t.Fatal(err)
		}
	}
	var live []catalog.Model
	for _, m := range models {
		live = append(live, catalog.Model{ID: m})
	}
	if len(live) > 0 {
		if err := catalog.SaveLive(id, chat, live); err != nil {
			t.Fatal(err)
		}
	}
	return up
}

// accWhoAnswered_f09 is the candidate id that answered the newest request
// (what rests after a failure: the provider, or one of its keys).
func accWhoAnswered_f09(t *testing.T, s *Server) (Route, string) {
	t.Helper()
	return grpLast_f09(t, s)
}

// Item 2: a saved group in order tries its first member, and falls to the
// second when the first answers 429; the trace names the group and lists its
// candidates in the members' order.
func TestAcceptance02GroupInOrderFallsToSecondMember_f09(t *testing.T) {
	grpIsolate_f09(t)
	b := accProvider_f09(t, "b", "", []string{"vendor/m"}, "sk-b")
	a := accProvider_f09(t, "a", "", []string{"m", "only-a"}, "sk-a")
	b.setCode("sk-b", 429)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code := grpChat_f09(t, s.Handler(), "group/mine", "acc-02", grpUser1_f09); code != 200 {
		t.Fatalf("group/mine: status %d", code)
	}
	r, who := accWhoAnswered_f09(t, s)
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Name != "Mine" || r.Group.Routing != "order" || r.Group.Auto {
		t.Fatalf("group trace: %+v", r.Group)
	}
	if !slices.Equal(r.Group.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Errorf("group members %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].ID != "b" || r.Order[1].ID != "a" {
		t.Errorf("candidate order %+v, want b then a", r.Order)
	}
	if who != "a" || len(r.Tries) != 2 || r.Tries[0].ID != "b" || r.Tries[0].Status != 429 {
		t.Errorf("tries %+v, answered by %s", r.Tries, who)
	}
	if len(b.hits) != 1 || len(a.hits) != 1 {
		t.Errorf("hits b=%v a=%v", b.hits, a.hits)
	}

	// the trace's group object carries every field the spec names
	raw, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	gj, _ := m["group"].(map[string]any)
	for _, k := range []string{"id", "name", "routing", "affinity", "auto", "members"} {
		if _, ok := gj[k]; !ok {
			t.Errorf("group trace lacks %q: %s", k, raw)
		}
	}
}

// Item 3 over HTTP: a request for the automatic group works; once the group is
// deleted it is a 404; showing it again restores it.
func TestAcceptance03AutoGroupDeleteAndShow_f09(t *testing.T) {
	grpIsolate_f09(t)
	accProvider_f09(t, "a", "", []string{"m", "only-a"}, "sk-a")
	accProvider_f09(t, "b", "", []string{"vendor/m"}, "sk-b")
	s := New()
	h := s.Handler()
	if code := grpChat_f09(t, h, "group/auto-m", "acc-03", grpUser1_f09); code != 200 {
		t.Fatalf("auto-m before delete: status %d", code)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code := grpChat_f09(t, h, "group/auto-m", "acc-03", grpUser1_f09); code != 404 {
		t.Fatalf("auto-m after delete: status %d, want 404", code)
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code := grpChat_f09(t, h, "group/auto-m", "acc-03", grpUser1_f09); code != 200 {
		t.Fatalf("auto-m after show: status %d", code)
	}
}

// A group none of whose models can be used answers 404 and says so.
func TestAcceptance04GroupWithNoReadyModelIs404_f09(t *testing.T) {
	grpIsolate_f09(t)
	if err := provider.SaveGroup(provider.Group{ID: "dead", Members: []string{"nowhere/x"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"group/dead","messages":[`+grpUser1_f09+`]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "没有模型就绪") {
		t.Fatalf("dead group: status %d body %s", rec.Code, rec.Body.String())
	}
}

// The model list an agent reads names a routing group as group/<id>.
func TestAcceptance01ModelListNamesGroup_f09(t *testing.T) {
	grpIsolate_f09(t)
	accProvider_f09(t, "a", "", []string{"m", "only-a"}, "sk-a")
	accProvider_f09(t, "b", "", []string{"vendor/m"}, "sk-b")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("models: %v: %s", err, rec.Body.String())
	}
	var ids []string
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	if !slices.Contains(ids, "group/auto-m") || !slices.Contains(ids, "a/m") || !slices.Contains(ids, "b/vendor/m") {
		t.Errorf("model list %v", ids)
	}
}

// Item 5: the same session, a tool-result request in the same turn, under the
// three affinity modes.
func TestAcceptance05AffinityByMode_f09(t *testing.T) {
	grpIsolate_f09(t)
	accProvider_f09(t, "dflt", provider.Rotate, nil, "sk-1", "sk-2")
	accProvider_f09(t, "sess", provider.Rotate, nil, "sk-1", "sk-2", "sk-3")
	accProvider_f09(t, "off", provider.Rotate, nil, "sk-1", "sk-2")
	if err := provider.SetAffinity("sess", "session"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("off", "off"); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	hist := []string{grpUser1_f09}
	tool := []string{grpCall1_f09, grpResult1_f09}
	with := func(parts ...[]string) []string {
		out := slices.Clone(hist)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	send := func(model string, msgs []string) {
		t.Helper()
		if code := grpChat_f09(t, h, model, "acc-05-"+model, msgs...); code != 200 {
			t.Fatalf("%s: status %d", model, code)
		}
	}

	// default: within the turn the tool result stays on the key; across a turn
	// without a cache read it moves on
	send("dflt/m", hist)
	r, first := accWhoAnswered_f09(t, s)
	if r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("default, first request: %+v", r.Affinity)
	}
	send("dflt/m", with(tool))
	r, who := accWhoAnswered_f09(t, s)
	if who != first || r.Affinity.Why != "turn" || !r.Affinity.Kept || !r.Affinity.Within {
		t.Errorf("default, tool result: answered %s (first %s), affinity %+v", who, first, r.Affinity)
	}
	send("dflt/m", with(tool, []string{grpUser2_f09}))
	if r, who := accWhoAnswered_f09(t, s); who == first || r.Affinity.Why != "no-cache" {
		t.Errorf("default, next turn without cache: answered %s, affinity %+v", who, r.Affinity)
	}

	// session: the conversation stays on its first key across turns
	hist = []string{grpUser1_f09}
	send("sess/m", hist)
	_, sFirst := accWhoAnswered_f09(t, s)
	send("sess/m", with(tool, []string{grpUser2_f09}))
	if r, who := accWhoAnswered_f09(t, s); who != sFirst || r.Affinity.Why != "session" || !r.Affinity.Kept {
		t.Errorf("session, turn 2: answered %s (first %s), affinity %+v", who, sFirst, r.Affinity)
	}
	send("sess/m", with(tool, []string{grpUser2_f09}, tool, []string{grpUser3_f09}))
	if r, who := accWhoAnswered_f09(t, s); who != sFirst || r.Affinity.Why != "session" {
		t.Errorf("session, turn 3: answered %s (first %s), affinity %+v", who, sFirst, r.Affinity)
	}

	// off: nothing is kept; the next turn moves on
	hist = []string{grpUser1_f09}
	send("off/m", hist)
	r, oFirst := accWhoAnswered_f09(t, s)
	if r.Affinity.Why != "off" || r.Affinity.Kept {
		t.Errorf("off, first request: affinity %+v", r.Affinity)
	}
	send("off/m", with([]string{grpUser2_f09}))
	if r, who := accWhoAnswered_f09(t, s); who == oFirst || r.Affinity.Why != "off" || r.Affinity.Kept {
		t.Errorf("off, next turn: answered %s (first %s), affinity %+v", who, oFirst, r.Affinity)
	}
}

// Item 6: under rotation, each new user turn moves to the next key (k1, k2,
// k3, k1); tool results within a turn and another model's request in between
// do not move it.
func TestAcceptance06RotationByTurn_f09(t *testing.T) {
	grpIsolate_f09(t)
	rot := accProvider_f09(t, "rot", provider.Rotate, []string{"m"}, "sk-1", "sk-2", "sk-3")
	accProvider_f09(t, "oth", provider.Rotate, []string{"x"}, "sk-o1", "sk-o2")
	s := New()
	h := s.Handler()
	tool := []string{grpCall1_f09, grpResult1_f09}
	hist := []string{grpUser1_f09}
	send := func(model string, msgs []string) {
		t.Helper()
		if code := grpChat_f09(t, h, model, "acc-06", msgs...); code != 200 {
			t.Fatalf("%s: status %d", model, code)
		}
	}
	send("rot/m", hist) // turn 1 opens on sk-1
	hist = append(slices.Clone(hist), tool...)
	send("rot/m", hist) // tool result, same turn
	send("oth/x", hist) // another model in between
	send("rot/m", hist) // another tool result, same turn
	hist = append(slices.Clone(hist), grpUser2_f09)
	send("rot/m", hist) // turn 2 moves to sk-2
	hist = append(slices.Clone(hist), tool...)
	send("rot/m", hist) // tool result, turn 2
	hist = append(slices.Clone(hist), grpUser3_f09)
	send("rot/m", hist) // turn 3 moves to sk-3
	hist = append(slices.Clone(hist), tool...)
	hist = append(hist, grpUser4_f09)
	send("rot/m", hist) // turn 4 back to sk-1

	want := []string{"sk-1", "sk-1", "sk-1", "sk-2", "sk-2", "sk-3", "sk-1"}
	if !slices.Equal(rot.hits, want) {
		t.Errorf("keys answering rot/m: %v, want %v", rot.hits, want)
	}
}

// Item 7: a conversation's answerer that is resting is passed over, the trace
// says resting; a new session identifier starts with first.
func TestAcceptance07RestingAnswererIsPassedOver_f09(t *testing.T) {
	grpIsolate_f09(t)
	accProvider_f09(t, "rs", provider.Rotate, nil, "sk-1", "sk-2")
	s := New()
	h := s.Handler()
	if code := grpChat_f09(t, h, "rs/m", "acc-07", grpUser1_f09); code != 200 {
		t.Fatalf("status %d", code)
	}
	_, who1 := accWhoAnswered_f09(t, s)
	t.Cleanup(func() {
		restingUntil.Lock()
		delete(restingUntil.m, who1)
		delete(restingUntil.note, who1)
		restingUntil.Unlock()
	})
	grpRestFor_f09(who1, time.Minute)

	if code := grpChat_f09(t, h, "rs/m", "acc-07", grpUser1_f09, grpCall1_f09, grpResult1_f09); code != 200 {
		t.Fatalf("status %d", code)
	}
	r, who2 := accWhoAnswered_f09(t, s)
	if who2 == who1 || r.Affinity == nil || r.Affinity.Why != "resting" || r.Affinity.Kept {
		t.Errorf("resting answerer %s: answered by %s, affinity %+v", who1, who2, r.Affinity)
	}

	if code := grpChat_f09(t, h, "rs/m", "acc-07-other", grpUser1_f09); code != 200 {
		t.Fatalf("status %d", code)
	}
	if r, _ := accWhoAnswered_f09(t, s); r.Affinity == nil || r.Affinity.Why != "first" {
		t.Errorf("another session: affinity %+v, want first", r.Affinity)
	}
}

// Item 9: a Chat request reaches only the key made for its protocol while it
// works; the keys set aside come after it, marked aside in the trace, and
// answer when the pool fails.
func TestAcceptance09ProtocolPoolAndAside_f09(t *testing.T) {
	grpIsolate_f09(t)
	pool := accProvider_f09(t, "acc09pool", provider.Rotate, []string{"m"}, "sk-1", "sk-2")
	if err := provider.AddKey("acc09pool", "", "sk-c", provider.Chat); err != nil {
		t.Fatal(err)
	}
	pool.keys = append(pool.keys, "sk-c") // the upstream names the key it was sent with
	s := New()
	h := s.Handler()

	if code := grpChat_f09(t, h, "acc09pool/m", "acc-09", grpUser1_f09); code != 200 {
		t.Fatalf("status %d", code)
	}
	r, who := accWhoAnswered_f09(t, s)
	if who != grpRest_f09("acc09pool", "sk-c") {
		t.Errorf("Chat request answered by %s, want the Chat key", who)
	}
	if len(r.Order) != 3 || r.Order[0].Aside || !r.Order[1].Aside || !r.Order[2].Aside {
		t.Fatalf("order %+v: want the Chat key first, then two aside", r.Order)
	}
	raw, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	order, _ := m["order"].([]any)
	if len(order) != 3 {
		t.Fatalf("order JSON: %s", raw)
	}
	if _, has := order[0].(map[string]any)["aside"]; has {
		t.Errorf("the pool's key is marked aside: %s", raw)
	}
	if order[1].(map[string]any)["aside"] != true {
		t.Errorf("aside key lacks aside:true in the trace: %s", raw)
	}

	// the pool fails: the keys set aside are tried, and the request succeeds
	pool.setCode("sk-c", 429)
	if code := grpChat_f09(t, h, "acc09pool/m", "acc-09-b", grpUser1_f09); code != 200 {
		t.Fatalf("with the pool failing: status %d", code)
	}
	if r, who := accWhoAnswered_f09(t, s); who == grpRest_f09("acc09pool", "sk-c") || len(r.Tries) != 2 || r.Tries[0].Status != 429 {
		t.Errorf("aside fallback: answered by %s, tries %+v", who, r.Tries)
	}
	if last := pool.hits[len(pool.hits)-1]; last != "sk-1" && last != "sk-2" {
		t.Errorf("aside key that answered: %s", last)
	}
}

// The trace names the affinity fields the spec lists.
func TestAcceptanceTraceAffinityFields_f09(t *testing.T) {
	grpIsolate_f09(t)
	accProvider_f09(t, "tf", provider.Rotate, nil, "sk-1", "sk-2")
	s := New()
	h := s.Handler()
	grpChat_f09(t, h, "tf/m", "acc-tf", grpUser1_f09)
	grpChat_f09(t, h, "tf/m", "acc-tf", grpUser1_f09, grpCall1_f09, grpResult1_f09)
	r, _ := accWhoAnswered_f09(t, s)
	raw, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	af, _ := m["affinity"].(map[string]any)
	for _, k := range []string{"mode", "turn", "within", "last", "cacheRead", "kept", "why"} {
		if _, ok := af[k]; !ok {
			t.Errorf("affinity trace lacks %q: %s", k, raw)
		}
	}
	if af["why"] != "turn" || af["kept"] != true || af["within"] != true || af["turn"] != float64(1) {
		t.Errorf("affinity trace: %v", af)
	}
}
