package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cachedKeys answers Chat per key, saying how many prompt tokens it read
// from its cache.
type cachedKeys struct {
	cached int
	seen   []string // the key of each request
}

func (k *cachedKeys) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.seen = append(k.seen, key)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"from-`+key+`","choices":[],"usage":{"prompt_tokens":3000,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":`+strconv.Itoa(k.cached)+`}}}`)
}

func affinityHome(t *testing.T) {
	groupsHome(t)
	sessions.Lock()
	sessions.last = map[string]answered{}
	sessions.Unlock()
	routed.Lock()
	routed.turn, routed.started, routed.used = map[string]int{}, map[string]turnStart{}, map[string]tokenUse{}
	routed.Unlock()
}

func sendAs(s *Server, session, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// threeKeys saves provider id with keys k1, k2, k3 behind up.
func threeKeys(t *testing.T, id, routing, affinity string, up http.Handler) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: id, Name: id, Chat: srv.URL + "/v1", Models: []string{"m", "small"}, Routing: routing,
		Affinity: affinity, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

const (
	aTurn1   = `{"model":"%s","messages":[{"role":"user","content":"fix it"}]}`
	aWithin1 = `{"model":"%s","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`
	aTurn2   = `{"model":"%s","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":"now test"}]}`
	aWithin2 = `{"model":"%s","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":"now test"},{"role":"assistant","content":null,"tool_calls":[{"id":"c2","type":"function","function":{"name":"go","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c2","content":"ok"}]}`
)

func withModel(f, model string) string { return strings.Replace(f, "%s", model, 1) }

func answeredBy(t *testing.T, s *Server, session, body string) string {
	t.Helper()
	code, out := sendAs(s, session, body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	i := strings.Index(out, "from-")
	if i < 0 {
		t.Fatalf("no answer: %s", out)
	}
	return out[i+5 : i+7]
}

// Least used moves every request on to another key; affinity holds it.
func TestAffinityModes(t *testing.T) {
	for _, c := range []struct {
		mode   string
		cached int
		reqs   []string
		// for the request after the first: who answers, and why
		same bool
		why  string
	}{
		{"", 0, []string{aTurn1, aWithin1}, true, "turn"},
		{"", 0, []string{aTurn1, aTurn2}, false, "no-cache"},
		{"", 2000, []string{aTurn1, aTurn2}, true, "cache"},
		{provider.AffinitySession, 0, []string{aTurn1, aTurn2}, true, "session"},
		{provider.AffinitySession, 0, []string{aTurn1, aWithin1}, true, "session"},
		{provider.AffinityTurn, 0, []string{aTurn1, aWithin1}, true, "turn"},
		{provider.AffinityTurn, 2000, []string{aTurn1, aTurn2}, false, "new-turn"},
		{provider.AffinityOff, 0, []string{aTurn1, aWithin1}, false, "off"},
	} {
		t.Run(c.mode+"/"+c.why, func(t *testing.T) {
			affinityHome(t)
			up := &cachedKeys{cached: c.cached}
			threeKeys(t, "lu", provider.LeastUsed, c.mode, up)
			s := New()
			first := answeredBy(t, s, "ses-1", withModel(c.reqs[0], "lu/m"))
			r := lastRoute(t, s)
			if r.Affinity == nil || r.Affinity.Why != "first" || r.Affinity.Kept || r.Affinity.Mode != c.mode || r.Affinity.Turn != 1 {
				t.Fatalf("first: %+v", r.Affinity)
			}
			second := answeredBy(t, s, "ses-1", withModel(c.reqs[1], "lu/m"))
			r = lastRoute(t, s)
			a := r.Affinity
			if a == nil || a.Why != c.why || a.Kept != c.same || (second == first) != c.same {
				t.Fatalf("second by %s after %s: %+v", second, first, a)
			}
			if a.Last != "lu#"+provider.KeyID(first) || a.CacheRead != c.cached {
				t.Fatalf("last: %+v", a)
			}
			if c.reqs[1] == aWithin1 && (!a.Within || a.Turn != 1) || c.reqs[1] == aTurn2 && (a.Within || a.Turn != 2) {
				t.Fatalf("turn: %+v", a)
			}
			if c.same && r.Order[0].ID != a.Last {
				t.Fatalf("kept but not first: %+v", r.Order)
			}
			js, _ := json.Marshal(r)
			if !bytes.Contains(js, []byte(`"affinity":{"mode":"`+c.mode+`","turn":`)) || !bytes.Contains(js, []byte(`"why":"`+c.why+`"`)) {
				t.Fatalf("trace %s", js)
			}
		})
	}
}

// Across turns, the cache counts only while warm.
func TestAffinityCold(t *testing.T) {
	affinityHome(t)
	up := &cachedKeys{cached: 5000}
	threeKeys(t, "lu", provider.LeastUsed, "", up)
	s := New()
	answeredBy(t, s, "ses-c", withModel(aTurn1, "lu/m"))
	sessions.Lock()
	for k, a := range sessions.last {
		a.at = a.at.Add(-cacheFresh - time.Minute)
		sessions.last[k] = a
	}
	sessions.Unlock()
	answeredBy(t, s, "ses-c", withModel(aTurn2, "lu/m"))
	if a := lastRoute(t, s).Affinity; a == nil || a.Why != "cold" || a.Kept {
		t.Fatalf("%+v", a)
	}
}

// In turn, it is the user's turn that moves on: tool results stay with the
// key their turn started on, and a request for another model in between
// leaves the turns alone.
func TestRotateByTurn(t *testing.T) {
	for _, mode := range []string{"", provider.AffinityOff} {
		t.Run("affinity="+mode, func(t *testing.T) {
			affinityHome(t)
			up := &cachedKeys{}
			threeKeys(t, "rot", provider.Rotate, mode, up)
			s := New()
			var got []string
			turns := []string{aTurn1, aTurn2,
				`{"model":"%s","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":"now test"},{"role":"assistant","content":"ok"},{"role":"user","content":"third"}]}`,
				`{"model":"%s","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":"now test"},{"role":"assistant","content":"ok"},{"role":"user","content":"third"},{"role":"assistant","content":"ok"},{"role":"user","content":"fourth"}]}`}
			for i, tn := range turns {
				k := answeredBy(t, s, "ses-r", withModel(tn, "rot/m"))
				got = append(got, k)
				// a small model's side request, in the same conversation
				answeredBy(t, s, "ses-r", withModel(`{"model":"%s","messages":[{"role":"user","content":"title?"}]}`, "rot/small"))
				if i < 2 {
					w := aWithin1
					if i == 1 {
						w = aWithin2
					}
					for range 2 {
						if wk := answeredBy(t, s, "ses-r", withModel(w, "rot/m")); wk != k {
							t.Fatalf("turn %d: tool result went to %s, the turn to %s", i+1, wk, k)
						}
					}
				}
			}
			if strings.Join(got, " ") != "k1 k2 k3 k1" {
				t.Fatalf("turns went to %v", got)
			}
		})
	}
}

// One resting is not kept; another conversation is one first seen.
func TestAffinityResting(t *testing.T) {
	affinityHome(t)
	up := &cachedKeys{}
	threeKeys(t, "ses", "", provider.AffinitySession, up)
	s := New()
	first := answeredBy(t, s, "ses-a", withModel(aTurn1, "ses/m"))
	if first != "k1" {
		t.Fatalf("first %s", first)
	}
	if k := answeredBy(t, s, "ses-a", withModel(aTurn2, "ses/m")); k != "k1" {
		t.Fatalf("session kept on %s", k)
	}
	k1 := "ses#" + provider.KeyID("k1")
	restingUntil.Lock()
	restingUntil.m[k1] = time.Now().Add(time.Minute)
	restingUntil.note[k1] = Rest{Why: failRate, Status: 429}
	restingUntil.Unlock()
	if k := answeredBy(t, s, "ses-a", withModel(aWithin2, "ses/m")); k == "k1" {
		t.Fatal("went to the resting key")
	}
	if a := lastRoute(t, s).Affinity; a == nil || a.Why != "resting" || a.Kept || a.Last != k1 {
		t.Fatalf("resting: %+v", a)
	}
	answeredBy(t, s, "ses-b", withModel(aWithin2, "ses/m"))
	if a := lastRoute(t, s).Affinity; a == nil || a.Why != "first" || a.Kept || a.Last != "" {
		t.Fatalf("another session: %+v", a)
	}
}

// One with no other to go to has no affinity to speak of.
func TestAffinityOneCandidate(t *testing.T) {
	affinityHome(t)
	up := &cachedKeys{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "one", Name: "One", Chat: srv.URL + "/v1", Key: "k1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	answeredBy(t, s, "x", withModel(aTurn1, "one/m"))
	answeredBy(t, s, "x", withModel(aWithin1, "one/m"))
	if a := lastRoute(t, s).Affinity; a != nil {
		t.Fatalf("%+v", a)
	}
}

func TestSessionOf(t *testing.T) {
	h := http.Header{}
	h.Set("x-claude-code-session-id", "cc")
	h.Set("session_id", "sid")
	s := sessionOf(h, provider.Chat, []byte(withModel(aWithin2, "m")), "m")
	if s.key != "sid\x00m" || s.turn != 2 || !s.within {
		t.Fatalf("%+v", s)
	}
	a := sessionOf(http.Header{}, provider.Anthropic, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"r"}]}]}`), "m")
	if a.turn != 1 || !a.within {
		t.Fatalf("anthropic %+v", a)
	}
	r := sessionOf(http.Header{}, provider.Responses, []byte(`{"model":"m","input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c","name":"x","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"r"}]}`), "m")
	if r.turn != 1 || !r.within {
		t.Fatalf("responses %+v", r)
	}
}
