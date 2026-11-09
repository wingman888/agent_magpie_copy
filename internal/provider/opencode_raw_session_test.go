package provider

import (
	"net/http"
	"testing"
)

// The agent's session id is sent exactly as the agent gave it, spaces and all;
// only a blank value counts as absent.
func TestOCRawSessionValueNotTrimmed(t *testing.T) {
	h := http.Header{}
	h.Set("x-session-id", " abc ")
	if got := OpenCodeSession(h, nil); got != " abc " {
		t.Errorf("session = %q, want the value unchanged %q", got, " abc ")
	}
}
