package provider

// Affinity: a conversation's follow-up requests go to the key or account
// that answered last, so the vendor's prompt cache can hit. How long that
// holds is the provider's Affinity; the gateway decides per request (see
// gateway/fallback.go and gateway/affinity).

import "strings"

// The affinity modes a provider may take. Empty is automatic.
const (
	AffinityAuto    = ""
	AffinitySession = "session" // the whole conversation stays put
	AffinityTurn    = "turn"    // only while one turn's tool results come back
	AffinityOff     = "off"     // never: each request is routed afresh
)

// normAffinity keeps a known affinity mode and falls back to automatic for
// anything else, as the config file may hold older or mistyped values.
func normAffinity(a string) string {
	switch a = strings.ToLower(strings.TrimSpace(a)); a {
	case AffinitySession, AffinityTurn, AffinityOff:
		return a
	}
	return AffinityAuto
}

// SetAffinity sets how long a provider keeps a conversation on one key or
// account; "" is automatic. An unknown mode falls back to automatic.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = normAffinity(affinity)
	return Save(*p)
}
