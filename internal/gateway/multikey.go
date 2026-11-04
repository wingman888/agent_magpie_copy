package gateway

// Several keys on one provider: the gateway tries the enabled keys in their
// order, one after another, and moves on under the same rule as a fallback —
// only while none of the reply has been sent, and only on the failures
// retryable already names. A key that just failed rests for a minute of its
// own, so its siblings keep their place. The provider as a whole rests only
// once every key of it has failed, the way it always has.

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// try is one upstream attempt: a provider, and the model to ask it for. When
// the provider has several enabled keys, p carries just the one this attempt
// sends, and key names it.
type try struct {
	p     provider.Provider
	model string
	multi bool // the provider has several enabled keys
	key   provider.EnabledKey
	last  bool // the last attempt of its provider
}

// tries expands the candidates into attempts, key by key. Within a provider
// the keys not resting go first, in their order; resting keys follow as a
// last resort. A provider with at most one enabled key is a single attempt,
// exactly as before.
func (s *Server) tries(cands []candidate) []try {
	var out []try
	for _, c := range cands {
		keys := c.p.KeysOn()
		if len(keys) <= 1 {
			out = append(out, try{p: c.p, model: c.model, last: true})
			continue
		}
		var ready, resting []try
		for _, k := range keys {
			t := try{p: narrow(c.p, k), model: c.model, multi: true, key: k}
			if s.resting(keyRest(c.p.ID, k.ID)) {
				resting = append(resting, t)
			} else {
				ready = append(ready, t)
			}
		}
		group := append(ready, resting...)
		group[len(group)-1].last = true
		out = append(out, group...)
	}
	return out
}

// narrow is the provider as seen by one of its keys: that key is the front
// one and no other key comes along.
func narrow(p provider.Provider, k provider.EnabledKey) provider.Provider {
	p.Key, p.KeyName, p.Keys = k.Key, k.Name, nil
	return p
}

// keyRest is the cooldown slot of one key of one provider. It shares the
// provider cooldown table, under a key no provider id can collide with.
func keyRest(providerID, keyID string) string {
	return providerID + "\x00key\x00" + keyID
}

// accountName names the account a failed attempt used, for the call record:
// "provider(name)" when the key has a name, else "provider(masked key)". The
// secret itself never appears.
func accountName(t try) string {
	if t.key.Name != "" {
		return t.p.ID + "(" + t.key.Name + ")"
	}
	return t.p.ID + "(" + provider.Mask(t.key.Key) + ")"
}

// scrub hides a secret that an upstream error message happened to echo.
func scrub(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, provider.Mask(key))
}
