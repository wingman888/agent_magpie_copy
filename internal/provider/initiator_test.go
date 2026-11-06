package provider

import "testing"

func TestInitiatorFollowsLastMessage(t *testing.T) {
	cases := map[string]string{
		`{"messages":[{"role":"user","content":"hi"}]}`:                     "user",
		`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`: "agent",
		`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"y"}]}`: "agent",
		`{"messages":[]}`: "agent",
		`{}`:              "agent",
	}
	for body, want := range cases {
		if got := initiator([]byte(body)); got != want {
			t.Errorf("initiator(%s) = %q, want %q", body, got, want)
		}
	}
}
