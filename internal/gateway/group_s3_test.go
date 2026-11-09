package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// s3Reset gives a test its own config and cache, and no resting candidates.
func s3Reset(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// s3Serve starts an upstream answering as h, and saves p to talk to it.
func s3Serve(t *testing.T, p provider.Provider, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p.Chat = up.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// s3Post sends one chat request through s, in the conversation session.
func s3Post(s *Server, session, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// s3Last is the newest route the trace holds.
func s3Last(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

// Chat messages of a conversation: a user turn, the tool call and its result
// sent back within it, the reply and the next user turn.
const (
	s3User   = `{"role":"user","content":"hi"}`
	s3Call   = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{}"}}]}`
	s3Result = `{"role":"tool","tool_call_id":"call_1","content":"ok"}`
	s3Done   = `{"role":"assistant","content":"done"}`
	s3Next   = `{"role":"user","content":"next"}`
)

// s3Body is a chat request for model with msgs as its history.
func s3Body(model string, msgs []string) string {
	return fmt.Sprintf(`{"model":%q,"messages":[%s]}`, model, strings.Join(msgs, ","))
}

// Acceptance 2: a routing group in order tries its first member, and when
// that one is rate limited the second answers; the trace names the group.
func TestS3GroupOrderFallsToSecondMember(t *testing.T) {
	s3Reset(t)
	b := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`}
	a := &fake{t: t, ctype: "application/json", reply: `{"id":"from-s3a","choices":[]}`}
	s3Serve(t, provider.Provider{ID: "s3b", Name: "S3 B", Key: "k-b", Models: []string{"vendor/m"}}, b)
	s3Serve(t, provider.Provider{ID: "s3a", Name: "S3 A", Key: "k-a", Models: []string{"only-a"}}, a)
	if err := provider.SaveGroup(provider.Group{ID: "s3mine", Members: []string{"s3b/vendor/m", "s3a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := s3Post(s, "", `{"model":"group/s3mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-s3a") {
		t.Fatalf("%d %s", code, body)
	}
	if b.got == nil {
		t.Fatal("the first member was not tried")
	}
	r := s3Last(t, s)
	if r.Group == nil || r.Group.ID != "s3mine" || r.Group.Routing != provider.Ordered {
		t.Fatalf("group in trace: %+v", r.Group)
	}
	if len(r.Group.Members) != 2 || r.Group.Members[0] != "s3b/vendor/m" || r.Group.Members[1] != "s3a/only-a" {
		t.Fatalf("members in trace: %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "s3b" || r.Order[1].Provider != "s3a" {
		t.Fatalf("order: %+v", r.Order)
	}
	if len(r.Tries) != 2 || r.Tries[0].ID != "s3b" || r.Tries[0].Fail != failRate || r.Tries[1].ID != "s3a" {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A group with no usable member answers 404 saying so.
func TestS3GroupWithNoModelReady(t *testing.T) {
	s3Reset(t)
	s := New()
	code, body := s3Post(s, "", `{"model":"group/s3ghost","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "has no model ready") {
		t.Fatalf("%d %s", code, body)
	}
}

// Acceptance 5: within a turn the conversation stays on the key that
// answered it (auto); across turns auto keeps it only with a vendor cache
// read, session keeps it always, and off never does.
func TestS3AffinityModes(t *testing.T) {
	s3Reset(t)
	up := &byKey{}
	s3Serve(t, provider.Provider{ID: "s3aff", Name: "S3 aff", Key: "s3-a1", Keys: []provider.KeyAccount{{Key: "s3-a2"}},
		Models: []string{"m"}, Routing: provider.LeastUsed}, up)
	s := New()
	send := func(session string, msgs ...string) (string, Route) {
		t.Helper()
		code, body := s3Post(s, session, s3Body("s3aff/m", msgs))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return up.seen[len(up.seen)-1], s3Last(t, s)
	}

	// default: within a turn the key answering the first request is kept
	k1, _ := send("s3-aff-default", s3User)
	if got, r := send("s3-aff-default", s3User, s3Call, s3Result); got != k1 || r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "turn" || !r.Affinity.Within {
		t.Fatalf("within a turn: key %s want %s, %+v", got, k1, r.Affinity)
	}
	// and across turns it is not, with nothing read from the vendor's cache
	if _, r := send("s3-aff-default", s3User, s3Call, s3Result, s3Done, s3Next); r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "no-cache" {
		t.Fatalf("across turns: %+v", r.Affinity)
	}

	// session: kept across turns too
	if err := provider.SetAffinity("s3aff", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	ks, _ := send("s3-aff-session", s3User)
	if got, r := send("s3-aff-session", s3User, s3Call, s3Result, s3Done, s3Next); got != ks || r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "session" {
		t.Fatalf("session: key %s want %s, %+v", got, ks, r.Affinity)
	}

	// off: never kept, even within a turn
	if err := provider.SetAffinity("s3aff", provider.AffinityOff); err != nil {
		t.Fatal(err)
	}
	send("s3-aff-off", s3User)
	if _, r := send("s3-aff-off", s3User, s3Call, s3Result); r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "off" {
		t.Fatalf("off: %+v", r.Affinity)
	}
}

// Acceptance 6: a rotating routing moves on one key per user turn, k1, k2,
// k3, k1; tool results within a turn stay on its key, and a request for
// another model in the same conversation does not move the turns.
func TestS3RotateByTurn(t *testing.T) {
	s3Reset(t)
	up := &byKey{}
	s3Serve(t, provider.Provider{ID: "s3rot", Name: "S3 rot", Key: "s3-r1", Keys: []provider.KeyAccount{{Key: "s3-r2"}, {Key: "s3-r3"}},
		Models: []string{"m", "x"}, Routing: provider.Rotate}, up)
	s := New()
	const sess = "s3-rot-conv"
	send := func(model string, msgs []string) string {
		t.Helper()
		code, body := s3Post(s, sess, s3Body(model, msgs))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return up.seen[len(up.seen)-1]
	}
	want := func(step, got, exp string) {
		t.Helper()
		if got != exp {
			t.Fatalf("%s: key %s, want %s (seen %v)", step, got, exp, up.seen)
		}
	}
	hist := []string{s3User}
	want("turn 1", send("s3rot/m", hist), "s3-r1")
	hist = append(hist, s3Call, s3Result)
	want("turn 1, tool result", send("s3rot/m", hist), "s3-r1")
	send("s3rot/x", hist) // another model in the same conversation moves no turn of m
	hist = append(hist, s3Done, s3Next)
	want("turn 2", send("s3rot/m", hist), "s3-r2")
	hist = append(hist, s3Call, s3Result)
	want("turn 2, tool result", send("s3rot/m", hist), "s3-r2")
	hist = append(hist, s3Done, s3Next)
	want("turn 3", send("s3rot/m", hist), "s3-r3")
	hist = append(hist, s3Call, s3Result)
	want("turn 3, tool result", send("s3rot/m", hist), "s3-r3")
	hist = append(hist, s3Done, s3Next)
	want("turn 4", send("s3rot/m", hist), "s3-r1")
}

// Acceptance 7: when the key that answered a conversation is resting, the
// conversation moves on with why "resting"; a new conversation is "first".
func TestS3AffinityResting(t *testing.T) {
	s3Reset(t)
	up := &byKey{}
	s3Serve(t, provider.Provider{ID: "s3rest", Name: "S3 rest", Key: "s3-q1", Keys: []provider.KeyAccount{{Key: "s3-q2"}},
		Models: []string{"m"}}, up)
	s := New()
	send := func(session string, msgs ...string) Route {
		t.Helper()
		code, body := s3Post(s, session, s3Body("s3rest/m", msgs))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return s3Last(t, s)
	}
	if send("s3-rest-1", s3User); up.seen[len(up.seen)-1] != "s3-q1" {
		t.Fatalf("first answer by %v", up.seen)
	}
	restingUntil.Lock()
	restingUntil.m["s3rest#"+provider.KeyID("s3-q1")] = time.Now().Add(time.Hour)
	restingUntil.Unlock()

	r := send("s3-rest-1", s3User, s3Call, s3Result)
	if up.seen[len(up.seen)-1] != "s3-q2" || r.Affinity == nil || r.Affinity.Why != "resting" || r.Affinity.Kept {
		t.Fatalf("resting: by %s, %+v", up.seen[len(up.seen)-1], r.Affinity)
	}
	r = send("s3-rest-2", s3User)
	if r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("new conversation: %+v", r.Affinity)
	}
}
