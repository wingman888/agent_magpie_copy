package provider

import "testing"

func TestF02InitiatorFollowsLastMessage(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"messages":[{"role":"user","content":"hi"}]}`, "user"},
		{`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, "agent"},
		{`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"y"}]}`, "agent"},
		{`{"messages":[]}`, "agent"},
		{`{}`, "agent"},
	}
	for _, c := range cases {
		if got := initiator([]byte(c.body)); got != c.want {
			t.Errorf("initiator(%s) = %q, want %q", c.body, got, c.want)
		}
	}
}
