package provider

// GroupPrefix starts the model id of a routing group ("group/<id>"), so no
// provider may take it as its id.
const GroupPrefix = "group"

// Affinity is how a provider keeps a conversation on the key or account that
// last answered it, so the vendor's prompt cache can hit (see gateway).
const (
	AffinityAuto    = ""        // kept within a turn; across turns, when the cache was read
	AffinitySession = "session" // kept for the whole conversation
	AffinityTurn    = "turn"    // kept within a turn only; a new turn may go elsewhere
	AffinityOff     = "off"     // never kept
)

// NormalizeAffinity returns s as an affinity is stored, "" (automatic) for
// anything unknown.
func NormalizeAffinity(s string) string {
	switch s {
	case AffinitySession, AffinityTurn, AffinityOff:
		return s
	}
	return AffinityAuto
}
