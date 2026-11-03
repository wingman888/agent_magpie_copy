package gateway

import "testing"

// A refused credential (401) and a bad request (400) are the caller's to see,
// not something another key or model should silently take over.
func TestRefusedCredentialIsNotFailedOver(t *testing.T) {
	for _, s := range []int{400, 401} {
		if keyFailover(s) {
			t.Errorf("HTTP %d fails over to another candidate", s)
		}
	}
	for _, s := range []int{402, 429, 503, 529} {
		if !keyFailover(s) {
			t.Errorf("HTTP %d does not fail over", s)
		}
	}
}

// A plain 403 (permission denied, forbidden) is returned as it is; only a 403
// whose text says the quota is used up moves on to another candidate.
func TestForbiddenFailsOverOnlyForQuota(t *testing.T) {
	if keyFailover(403) {
		t.Error("a bare 403 is treated as a failover status")
	}
	for _, msg := range []string{
		"",
		"permission denied",
		"forbidden: access to this model is not allowed",
		"insufficient permissions for this resource",
	} {
		if failoverReply(403, msg) {
			t.Errorf("plain 403 %q fails over", msg)
		}
	}
	for _, msg := range []string{
		"You exceeded your current quota",
		"insufficient_quota",
		"insufficient balance",
		"quota exhausted",
		"额度已用完",
		"余额不足",
	} {
		if !failoverReply(403, msg) {
			t.Errorf("quota 403 %q does not fail over", msg)
		}
	}
}
