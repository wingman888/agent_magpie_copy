package gateway

import "github.com/yetone/magpie/internal/provider"

// Protocol tiers rank a key by how well it suits a request's protocol, best
// first. Routing, rotation and affinity work only within the best tier that
// has a usable key; the keys of the other tiers are tried after those, as
// asides.
const (
	tierMatch = 0 // the key is made for the request's own protocol
	tierAny   = 1 // the key says no protocol
	tierOther = 2 // the key is made for another protocol
)

// protocolTier places a key for a request of protocol req. The key's own
// protocol counts; when it has none, providerKeyProto stands in for it. Pass
// the provider's KeyProtocol only for the primary key, which is the one that
// field describes; any other key passes "".
func protocolTier(req, keyProto, providerKeyProto provider.Protocol) int {
	k := keyProto
	if k == "" {
		k = providerKeyProto
	}
	switch k {
	case "":
		return tierAny
	case req:
		return tierMatch
	}
	return tierOther
}
