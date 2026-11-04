package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestProtocolTier(t *testing.T) {
	cases := []struct {
		name                     string
		req, keyProto, provProto provider.Protocol
		want                     int
	}{
		{"key matches the request", provider.Chat, provider.Chat, "", tierMatch},
		{"key made for another protocol", provider.Chat, provider.Anthropic, "", tierOther},
		{"key says no protocol", provider.Chat, "", "", tierAny},
		{"no key protocol, provider's KeyProtocol matches", provider.Anthropic, "", provider.Anthropic, tierMatch},
		{"no key protocol, provider's KeyProtocol differs", provider.Chat, "", provider.Responses, tierOther},
		{"key's own protocol wins over the provider's", provider.Chat, provider.Chat, provider.Anthropic, tierMatch},
	}
	for _, c := range cases {
		if got := protocolTier(c.req, c.keyProto, c.provProto); got != c.want {
			t.Errorf("%s: protocolTier = %d, want %d", c.name, got, c.want)
		}
	}
}
