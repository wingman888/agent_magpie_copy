package usage

import (
	"testing"
	"time"
)

func TestAgentOf(t *testing.T) {
	cases := map[string]string{
		"claude-cli/2.1.0 (external, cli)":         "claude",
		"codex_cli_rs/0.40.0 (Mac OS 26.0; arm64)": "codex",
		"GeminiCLI/0.9.0 (darwin; arm64)":          "gemini",
		"opencode/1.2.3":                           "opencode",
		"curl/8.4.0":                               "curl",
		"":                                         "other",
	}
	for ua, want := range cases {
		if got := AgentOf(ua); got != want {
			t.Errorf("%q: got %q want %q", ua, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 30, 0, 0, time.UTC)
	recs := []Record{
		{Time: now.Add(-40 * 24 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Input: 10, Output: 1},
		{Time: now.Add(-3 * 24 * time.Hour), Agent: "claude", Provider: "p", Model: "m", Input: 100, Output: 20},
		{Time: now.Add(-2 * time.Hour), Agent: "claude", Provider: "p", Model: "m2", Input: 1000, Output: 200, Status: 200},
		{Time: now.Add(-1 * time.Hour), Agent: "codex", Provider: "p", Model: "m", Status: 502},
	}
	s := summarize(Today, now, recs)
	if s.Calls != 2 || s.Errors != 1 || s.Input != 1000 || s.Bucket != "hour" || len(s.Series) != 24 {
		t.Fatalf("today: %+v", s.Totals)
	}
	if s.Series[13].Input != 1000 || s.Series[14].Calls != 1 {
		t.Fatalf("today buckets: %+v %+v", s.Series[13].Totals, s.Series[14].Totals)
	}
	s = summarize(Week, now, recs)
	if s.Calls != 3 || len(s.Series) != 7 || s.Series[3].Input != 100 || s.Series[6].Input != 1000 {
		t.Fatalf("week: %+v series=%d", s.Totals, len(s.Series))
	}
	if s.Agents[0].ID != "claude" || s.Agents[0].Input != 1100 || s.Models[0].ID != "p/m2" {
		t.Fatalf("groups: %+v %+v", s.Agents, s.Models)
	}
	s = summarize(All, now, recs)
	if s.Calls != 4 || s.Bucket != "day" || len(s.Series) != 41 {
		t.Fatalf("all: %+v bucket=%s series=%d", s.Totals, s.Bucket, len(s.Series))
	}
	recs = append([]Record{{Time: now.Add(-100 * 24 * time.Hour), Agent: "pi", Provider: "p", Model: "m", Input: 1}}, recs...)
	s = summarize(All, now, recs)
	if s.Bucket != "week" || s.Since.Weekday() != time.Monday || s.Calls != 5 {
		t.Fatalf("all/weeks: bucket=%s since=%s calls=%d", s.Bucket, s.Since, s.Calls)
	}
	if s.Unpriced != 4 || s.Cost != 0 {
		t.Fatalf("pricing: unpriced=%d cost=%v", s.Unpriced, s.Cost)
	}
}
