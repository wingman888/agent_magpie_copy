package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An account's allowance keeps the windows that can stop it, each with
// when it resets.
func TestAllowanceOf(t *testing.T) {
	now := time.Now()
	week := now.Add(3 * 24 * time.Hour)
	a := allowanceOf([]QuotaWindow{
		{Name: "5 hours", Used: 80, ResetSecs: 2 * 3600, Span: 5 * time.Hour},
		{Name: "7 days", Used: 30, ResetsAt: &week, Span: 7 * 24 * time.Hour},
		{Name: "On-demand", Used: 100, ResetsAt: &week, Aside: true},
	}, now)
	if len(a) != 2 || !a[0].Resets.Equal(now.Add(2*time.Hour)) || !a[1].Resets.Equal(week) {
		t.Fatalf("%+v", a)
	}
	used, renews := a.For("m", now)
	if used != 80 || len(renews) != 2 || !renews[0].Equal(week) {
		t.Fatalf("the fullest decides, the week renews first: %v %v", used, renews)
	}
	if a = allowanceOf([]QuotaWindow{{Used: 10}}, now); !a[0].Resets.IsZero() {
		t.Fatalf("not known: %+v", a)
	}
}

// A window counts only the models it is for, and one whose reset has
// passed is empty again.
func TestAllowanceFor(t *testing.T) {
	now := time.Now()
	a := Allowance{
		{Used: 20, Resets: now.Add(2 * time.Hour), Span: 5 * time.Hour},
		{Used: 40, Resets: now.Add(4 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
		{Used: 100, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	if used, renews := a.For("claude-sonnet-5", now); used != 40 || len(renews) != 2 {
		t.Fatalf("sonnet: %v %v", used, renews)
	}
	if used, renews := a.For("claude-opus-5-5", now); used != 100 || len(renews) != 3 || !renews[0].Equal(a[1].Resets) {
		t.Fatalf("opus: %v %v", used, renews)
	}
	if got := a.Full("claude-opus-5-5", 98, now); !got.Equal(a[2].Resets) {
		t.Fatalf("opus full until %v", got)
	}
	if got := a.Full("claude-sonnet-5", 98, now); !got.IsZero() {
		t.Fatalf("sonnet isn't full: %v", got)
	}
	a[0].Used, a[0].Resets = 100, now.Add(-time.Minute)
	if used, renews := a.For("claude-sonnet-5", now); used != 40 || !renews[1].IsZero() {
		t.Fatalf("renewed five hours: %v %v", used, renews)
	}
}

// The first ask for an agent's allowances waits for them, so the first
// requests after magpie starts are routed by them too.
func TestAllowancesFirstWaits(t *testing.T) {
	if m := Allowances("nobody"); m == nil {
		t.Fatal("the first ask didn't wait")
	}
}

// Copilot's allowances renew on the day GitHub says, and its code
// completions don't stop an account from answering.
func TestCopilotUsageResets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"copilot_plan":"individual","quota_reset_date":"2026-10-01","quota_reset_date_utc":"2026-10-01T00:00:00.000Z",
			"quota_snapshots":{"completions":{"has_quota":true,"entitlement":2000,"quota_remaining":0},
			"premium_interactions":{"has_quota":true,"entitlement":300,"quota_remaining":75}}}`))
	}))
	defer srv.Close()
	old := CopilotUserURL
	CopilotUserURL = srv.URL
	defer func() { CopilotUserURL = old }()
	q := copilotSubscriptionUsage(t.Context(), "tok")
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[1].ResetsAt == nil || q.Windows[1].ResetsAt.Month() != time.October {
		t.Fatalf("%+v", q)
	}
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	if used, renews := allowanceOf(q.Windows, now).For("gpt-5", now); used != 75 || len(renews) != 1 {
		t.Fatalf("completions counted: %v %v", used, renews)
	}
}
