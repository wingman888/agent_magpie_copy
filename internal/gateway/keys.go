package gateway

// Several keys per provider: each is an account of the same vendor, tried in
// the order the provider lists them. A key that just failed rests for the
// same minute a provider does, on its own, so the other keys of that
// provider are not held back. A provider with one enabled key takes the path
// it always took, and nothing here applies to it.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// try is one attempt: a candidate provider, with the key to send when the
// provider has several enabled keys.
type try struct {
	c     candidate
	key   provider.KeyCred
	keyed bool
}

var restingKeys = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// keyRest is the key in the rest map: the provider and a hash of the key, so
// the key itself is never kept in the map.
func keyRest(providerID, key string) string {
	sum := sha256.Sum256([]byte(key))
	return providerID + "\x00" + hex.EncodeToString(sum[:])
}

func (s *Server) restingKey(providerID, key string) bool {
	restingKeys.Lock()
	defer restingKeys.Unlock()
	return time.Now().Before(restingKeys.m[keyRest(providerID, key)])
}

func (s *Server) restKey(providerID, key string) {
	restingKeys.Lock()
	restingKeys.m[keyRest(providerID, key)] = time.Now().Add(fallbackCooldown)
	restingKeys.Unlock()
}

// multiKeys returns the enabled keys of a provider when it has more than one,
// nil otherwise. Signed-in accounts never use keys.
func multiKeys(p provider.Provider) []provider.KeyCred {
	if p.Account != nil {
		return nil
	}
	creds := p.KeysOn()
	if len(creds) <= 1 {
		return nil
	}
	return creds
}

// providerResting says a provider with several keys rests only when every
// one of its enabled keys rests; otherwise it is the provider's own cooldown.
func (s *Server) providerResting(p provider.Provider) bool {
	creds := multiKeys(p)
	if creds == nil {
		return s.resting(p.ID)
	}
	for _, k := range creds {
		if !s.restingKey(p.ID, k.Key) {
			return false
		}
	}
	return true
}

// tries expands candidates into single attempts. Within a provider with
// several enabled keys, keys that are ready go first, in order, and resting
// keys follow.
func (s *Server) tries(cands []candidate) []try {
	var out []try
	for _, c := range cands {
		creds := multiKeys(c.p)
		if creds == nil {
			out = append(out, try{c: c})
			continue
		}
		var ready, resting []try
		for _, k := range creds {
			t := try{c: c, key: k, keyed: true}
			if s.restingKey(c.p.ID, k.Key) {
				resting = append(resting, t)
			} else {
				ready = append(ready, t)
			}
		}
		out = append(out, ready...)
		out = append(out, resting...)
	}
	return out
}

// label names the account a failed attempt used: the provider with the key's
// name, or with its masked key when it has none.
func (t try) label() string {
	if t.key.Name != "" {
		return t.c.p.ID + "(" + t.key.Name + ")"
	}
	return t.c.p.ID + "(" + provider.Mask(t.key.Key) + ")"
}

// scrub keeps a full key out of a message, showing its mask instead.
func scrub(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, provider.Mask(key))
}
