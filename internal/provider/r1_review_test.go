package provider

import (
	"os"
	"reflect"
	"testing"
)

// A key limited to one protocol is planned only on that protocol's endpoint,
// even when the vendor has both OpenAI-style endpoints.
func TestR1LimitedKeyUsesOnlyItsEndpoint(t *testing.T) {
	s2Env(t)
	p := Provider{ID: "r1both", Name: "R1 Both", Chat: "https://relay.example/v1",
		Responses: "https://relay.example/v1", Anthropic: "https://relay.example",
		Key: "sk-r1-any",
		Keys: []ExtraKey{
			{Key: "sk-r1-resp", Protocol: Responses},
			{Key: "sk-r1-chat", Protocol: Chat},
			{Key: "sk-r1-ant", Protocol: Anthropic},
		}}
	want := map[string][]Protocol{
		"sk-r1-any":  p.Speaks(),
		"sk-r1-resp": {Responses},
		"sk-r1-chat": {Chat},
		"sk-r1-ant":  {Anthropic},
	}
	for _, from := range []Protocol{Chat, Responses, Anthropic} {
		for _, pl := range p.KeyPlans(from, "deepseek-r1") {
			if got := pl.Upstreams; !reflect.DeepEqual(got, want[pl.Provider.Key]) {
				t.Fatalf("from %s: key %s planned on %v, want %v", from, pl.Provider.Key, got, want[pl.Provider.Key])
			}
		}
	}
}

// A key limited to a protocol the vendor has no endpoint for is not planned.
func TestR1LimitedKeyWithoutEndpointIsSkipped(t *testing.T) {
	s2Env(t)
	p := Provider{ID: "r1chat", Name: "R1 Chat", Chat: "https://relay.example/v1",
		Key: "sk-r1-chat", KeyProtocol: Chat,
		Keys: []ExtraKey{{Key: "sk-r1-ant", Protocol: Anthropic}}}
	for _, pl := range p.KeyPlans(Anthropic, "claude-r1") {
		if pl.Provider.Key == "sk-r1-ant" {
			t.Fatal("an anthropic key was planned on a vendor with no anthropic endpoint")
		}
	}
}

// Save keeps headers and fallback a caller left nil, and clears them when the
// caller gives an empty list or map.
func TestR1SaveKeepsUnsetHeadersAndFallback(t *testing.T) {
	s2Env(t)
	base := Provider{ID: "r1save", Name: "R1 Save", Chat: "https://relay.example/v1", Key: "sk-r1-save",
		Headers: map[string]string{"X-Org-Id": "acme"}, Fallback: []string{"backup/m2"}}
	if err := Save(base); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "r1save", Name: "R1 Save", Chat: "https://relay.example/v1", Key: "sk-r1-save"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("r1save")
	if err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || !reflect.DeepEqual(p.Fallback, []string{"backup/m2"}) {
		t.Fatalf("unset fields were cleared: headers %v fallback %v", p.Headers, p.Fallback)
	}
	cleared := *p
	cleared.Headers = map[string]string{}
	cleared.Fallback = []string{""}
	if err := Save(cleared); err != nil {
		t.Fatal(err)
	}
	p, err = Find("r1save")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 0 || len(p.Fallback) != 0 {
		t.Fatalf("explicit empties did not clear: headers %v fallback %v", p.Headers, p.Fallback)
	}
}

// The signed-in account keeps its refresh token only in the agent's own
// store; a copy appears when magpie switches away from it, and not before.
func TestR1SignedInAccountHasNoCopy(t *testing.T) {
	s3Env(t)
	s3CodexAuth(t, "a@example.com", "a")
	if err := observeSignIns(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stashPath("codex", "a@example.com")); !os.IsNotExist(err) {
		t.Fatalf("the signed-in account has a copy of its sign-in: %v", err)
	}
	s3CodexAuth(t, "b@example.com", "b")
	if err := observeSignIns(true); err != nil {
		t.Fatal(err)
	}
	// a was replaced outside magpie, so it has no copy to switch back to
	if _, err := SwitchAccount("codex", "a@example.com"); err == nil {
		t.Fatal("switched to an account magpie holds no sign-in for")
	}
}

// Switching away from an account saves its newest sign-in first.
func TestR1SwitchSavesTheOutgoingSignIn(t *testing.T) {
	s3Env(t)
	s3TwoAccounts(t)
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	// b is signed in and has no copy yet
	if _, err := os.Stat(stashPath("codex", "b@example.com")); !os.IsNotExist(err) {
		t.Fatalf("the signed-in account has a copy before any switch: %v", err)
	}
	if _, err := SwitchAccount("codex", "a@example.com"); err != nil {
		t.Fatalf("switch to a: %v", err)
	}
	if n := s3StashName(t, "b@example.com"); n != "b" {
		t.Fatalf("the account switched away from kept %q, want b", n)
	}
}
