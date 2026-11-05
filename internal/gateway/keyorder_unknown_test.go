package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A key limited to another protocol needs a conversion just as an unlimited
// one does, so for a model of unknown family both rank 1 and keep their order.
func TestUnknownModelOtherProtocolKeyRanksWithUnlimited(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay",
		Chat: "http://chat.invalid/v1", Anthropic: "http://anth.invalid",
		Key: "A", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{{Key: "U"}}}
	ks, ok := usableKeys(p, "deepseek-chat", provider.Anthropic, p.KeysOn())
	if !ok || keyNames(ks) != "A,U" {
		t.Fatalf("usable keys %q (ok %v), want A,U", keyNames(ks), ok)
	}
	// the key that speaks the client's protocol directly still goes first
	ks, ok = usableKeys(p, "deepseek-chat", provider.Chat, p.KeysOn())
	if !ok || keyNames(ks) != "A,U" {
		t.Fatalf("chat client: usable keys %q (ok %v), want A,U", keyNames(ks), ok)
	}
	// and an Anthropic-limited key, for a chat client, goes behind the Chat one
	q := provider.Provider{ID: "relay", Name: "Relay",
		Chat: "http://chat.invalid/v1", Anthropic: "http://anth.invalid",
		Key: "U", Keys: []provider.KeyAccount{{Key: "C", Protocol: provider.Chat}, {Key: "N", Protocol: provider.Anthropic}}}
	ks, ok = usableKeys(q, "deepseek-chat", provider.Chat, q.KeysOn())
	if !ok || keyNames(ks) != "C,U,N" {
		t.Fatalf("usable keys %q (ok %v), want C,U,N", keyNames(ks), ok)
	}
}
