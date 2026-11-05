package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// flush with nothing written must answer 502, not a silent 200.
func TestAccAttemptFlushWithNoReplyIs502(t *testing.T) {
	rec := httptest.NewRecorder()
	a := &attemptWriter{dst: rec}
	a.flush()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
}

// markResponseOnly works on a Server whose map was never made.
func TestAccMarkResponseOnlyInitialisesMap(t *testing.T) {
	s := &Server{}
	s.markResponseOnly("relay", "m1")
	if !s.responseOnlyModel("relay", "m1") {
		t.Fatal("model not marked response-only")
	}
	if s.responseOnlyModel("relay", "other") {
		t.Fatal("unrelated model marked response-only")
	}
}
