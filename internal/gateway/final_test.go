package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Message bodies of one Chat conversation, as the agent sends them.
const (
	finalUser   = `{"role":"user","content":"hi"}`
	finalAgain  = `{"role":"user","content":"again"}`
	finalCall   = `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}`
	finalResult = `{"role":"tool","tool_call_id":"c1","content":"out"}`
)

// finalChatBody is a Chat request body holding the messages given.
func finalChatBody(msgs ...string) string {
	return `{"model":"x","messages":[` + strings.Join(msgs, ",") + `]}`
}

// finalPlan plans one request of a conversation, from its real body, and
// remembers its first candidate as the conversation's answerer, as a
// successful answer would.
func finalPlan(s *Server, p provider.Provider, model, conv, body string) []candidate {
	tc := turnOf(provider.Chat, []byte(body), conv)
	cs, pl := s.planTurn(p, model, provider.Chat, tc)
	if len(cs) > 0 {
		rememberSticky(conv, pl.scope, cs[0], 0)
	}
	return cs
}

// Rotation by turns, read from real request bodies: the tool results of a
// turn, and another model's request in between, do not move it on.
func TestFinalRotateFollowsTurnsFromBodies(t *testing.T) {
	p := provider.Provider{ID: "finalrot", Chat: "https://finalrot.example/v1", Key: "final-k1", Routing: provider.Rotate,
		Keys: []provider.KeyAccount{{Key: "final-k2"}, {Key: "final-k3"}}}
	s := &Server{}
	const conv = "final-conv-rot"
	turn1 := finalChatBody(finalUser)
	turn1Tool := finalChatBody(finalUser, finalCall, finalResult)
	turn2 := finalChatBody(finalUser, finalCall, finalResult, finalAgain)
	turn2Tool := finalChatBody(finalUser, finalCall, finalResult, finalAgain, finalCall, finalResult)
	turn3 := finalChatBody(finalUser, finalCall, finalResult, finalAgain, finalCall, finalResult, finalAgain)

	var got []string
	ask := func(model, body string) {
		t.Helper()
		cs := finalPlan(s, p, model, conv, body)
		if len(cs) == 0 {
			t.Fatal("no candidates")
		}
		got = append(got, cs[0].p.Key)
	}
	ask("final-m", turn1)
	ask("final-m", turn1Tool)
	ask("final-other", turn1Tool) // another model's request in between
	ask("final-m", turn2)
	ask("final-m", turn2Tool)
	ask("final-m", turn3)
	// the other model's request starts on its own rotation's first key, and
	// leaves the conversation's own sequence as it was
	if want := "final-k1 final-k1 final-k1 final-k2 final-k2 final-k3"; strings.Join(got, " ") != want {
		t.Fatalf("rotation by turns: %v, want %s", got, want)
	}
}

// A key made for another protocol than the one the model is at home in
// comes last, behind the keys that suit it, and rotation is among those.
func TestFinalProtocolPoolAsideLast(t *testing.T) {
	p := provider.Provider{ID: "finalpool", Chat: "https://finalpool.example/v1", Key: "final-u1", Routing: provider.Rotate,
		Keys: []provider.KeyAccount{{Key: "final-u2"}, {Key: "final-c3", Protocol: provider.Chat}}}
	s := &Server{}
	const conv = "final-conv-pool"
	const model = "claude-final-5-5"
	bodies := []string{
		finalChatBody(finalUser),
		finalChatBody(finalUser, finalCall, finalResult, finalAgain),
		finalChatBody(finalUser, finalCall, finalResult, finalAgain, finalCall, finalResult, finalAgain),
	}
	var got []string
	for i, body := range bodies {
		cs := finalPlan(s, p, model, conv, body)
		if len(cs) != 3 {
			t.Fatalf("turn %d: %d candidates, want 3", i+1, len(cs))
		}
		if cs[2].p.Key != "final-c3" || !cs[2].aside {
			t.Fatalf("turn %d: last candidate %s aside=%v, want the Chat key aside", i+1, cs[2].p.Key, cs[2].aside)
		}
		if cs[0].aside || cs[1].aside {
			t.Fatalf("turn %d: keys that suit the request marked aside", i+1)
		}
		got = append(got, cs[0].p.Key)
	}
	if want := "final-u1 final-u2 final-u1"; strings.Join(got, " ") != want {
		t.Fatalf("main pool rotation: %v, want %s", got, want)
	}
}
