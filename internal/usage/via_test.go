package usage

import (
	"testing"
	"time"
)

// Calls an agent made for a routing group are not counted in the summary.
func TestUviaSummarizeSkipsGroupCalls(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-1 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 100, Output: 10, Status: 200},
		{Time: now.Add(-1 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 900, Output: 90, Status: 200, Via: "group/mine"},
	}
	s := summarize(Month, now, recs)
	if s.Calls != 1 || s.Input != 100 {
		t.Fatalf("group call counted: calls=%d input=%d", s.Calls, s.Input)
	}
}
