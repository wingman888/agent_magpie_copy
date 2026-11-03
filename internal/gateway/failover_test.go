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
	for _, s := range []int{402, 403, 429, 503, 529} {
		if !keyFailover(s) {
			t.Errorf("HTTP %d does not fail over", s)
		}
	}
}
