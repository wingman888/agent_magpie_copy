package provider

import "testing"

// keyFor picks, for each endpoint, the first key in use that may speak it.
func TestKeyForPicksKeyThatMaySpeakEndpoint(t *testing.T) {
	p := Provider{
		Key:         "chat-key",
		KeyProtocol: Chat,
		Keys: []KeyAccount{
			{Key: "anthropic-key", Protocol: Anthropic},
			{Key: "any-key"},
		},
	}
	cases := []struct {
		proto Protocol
		want  string
		ok    bool
	}{
		{Chat, "chat-key", true},
		{Anthropic, "anthropic-key", true},
		{Responses, "any-key", true},
	}
	for _, c := range cases {
		k, ok := p.keyFor(c.proto)
		if ok != c.ok || k.Key != c.want {
			t.Errorf("keyFor(%s) = %q, %v; want %q, %v", c.proto, k.Key, ok, c.want, c.ok)
		}
	}
}

// With only protocol-limited keys and none for the endpoint, nothing is picked.
func TestKeyForSkipsEndpointNoKeyMaySpeak(t *testing.T) {
	p := Provider{Key: "chat-key", KeyProtocol: Chat}
	if k, ok := p.keyFor(Anthropic); ok {
		t.Errorf("keyFor(anthropic) = %q; want none", k.Key)
	}
}

// A provider with no key keeps the old behaviour: the empty first key.
func TestKeyForNoKeyKeepsOldBehaviour(t *testing.T) {
	p := Provider{}
	k, ok := p.keyFor(Chat)
	if !ok || k.Key != "" {
		t.Errorf("keyFor on keyless provider = %q, %v; want \"\", true", k.Key, ok)
	}
}
