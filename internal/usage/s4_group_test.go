package usage

import (
	"testing"
	"time"
)

func TestS4GroupCallsLeaveProviderUsage(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-time.Hour), Agent: "claude", Provider: "a", Model: "m", Input: 100, Output: 10, Status: 200},
		{Time: now.Add(-time.Hour), Agent: "claude", Provider: "b", Model: "m", Input: 50, Output: 5, Status: 200, Group: "group/mine"},
	}
	s := summarize(Today, now, recs)
	if s.Calls != 2 || s.Input != 150 {
		t.Fatalf("totals should still count group calls: %+v", s.Totals)
	}
	if len(s.Agents) != 1 || s.Agents[0].Input != 150 {
		t.Fatalf("agents should still count group calls: %+v", s.Agents)
	}
	if len(s.Models) != 1 || s.Models[0].ID != "a/m" {
		t.Fatalf("provider usage must not count group calls: %+v", s.Models)
	}
}
