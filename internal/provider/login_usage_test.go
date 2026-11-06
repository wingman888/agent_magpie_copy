package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubLogin replaces the account lookup, the fetch and the clock for one test,
// and clears the cache before and after it.
func stubLogin(t *testing.T, users []string, fetch func(ctx context.Context, agent, user string) (SubscriptionQuota, error)) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	nowp := &now
	oldUsers, oldFetch, oldClock := loginUsers, usageFetch, loginUsageClock
	loginUsers = func(agent string) []string { return users }
	usageFetch = fetch
	loginUsageClock = func() time.Time { return *nowp }
	reset := func() {
		loginUsageCache.Lock()
		loginUsageCache.data, loginUsageCache.at = nil, nil
		loginUsageCache.Unlock()
	}
	reset()
	t.Cleanup(func() {
		loginUsers, usageFetch, loginUsageClock = oldUsers, oldFetch, oldClock
		reset()
	})
	return &now
}

func quotaOf(agent, user string, five, week float64) SubscriptionQuota {
	return SubscriptionQuota{Provider: agent, Name: user, Windows: []QuotaWindow{
		{Name: "5 hours", Used: five}, {Name: "7 days", Used: week},
	}}
}

func TestLoginUsageReportsEachAccountAndCaches(t *testing.T) {
	calls := 0
	stubLogin(t, []string{"a@example.com", "b@example.com"}, func(_ context.Context, agent, user string) (SubscriptionQuota, error) {
		calls++
		if user == "a@example.com" {
			return quotaOf(agent, user, 40, 10), nil
		}
		return quotaOf(agent, user, 5, 70), nil
	})
	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2: %v", len(got), got)
	}
	if a := got["a@example.com"]; a.FiveHour == nil || *a.FiveHour != 40 || a.Weekly == nil || *a.Weekly != 10 {
		t.Fatalf("a's shares = %v / %v, want 40 / 10", a.FiveHour, a.Weekly)
	}
	if b := got["b@example.com"]; b.Weekly == nil || *b.Weekly != 70 {
		t.Fatalf("b's weekly = %v, want 70", b.Weekly)
	}

	again := LoginUsage(context.Background(), "codex")
	if len(again) != 2 || calls != 2 {
		t.Fatalf("second call within a minute fetched again: %d fetches, %d accounts", calls, len(again))
	}
}

func TestLoginUsageKeepsPriorResultWhenAccountFails(t *testing.T) {
	fail := false
	now := stubLogin(t, []string{"a@example.com", "b@example.com"}, func(_ context.Context, agent, user string) (SubscriptionQuota, error) {
		if fail && user == "b@example.com" {
			return SubscriptionQuota{}, errors.New("boom")
		}
		return quotaOf(agent, user, 25, 60), nil
	})
	first := LoginUsage(context.Background(), "claude")

	fail = true
	*now = now.Add(2 * time.Minute)
	second := LoginUsage(context.Background(), "claude")
	if _, ok := second["b@example.com"]; !ok {
		t.Fatalf("failed account dropped from the result")
	}
	b, want := second["b@example.com"], first["b@example.com"]
	if b.FiveHour == nil || want.FiveHour == nil || *b.FiveHour != *want.FiveHour || b.Error != "" {
		t.Fatalf("failed account = %+v, want its prior result %+v", b, want)
	}
	if a := second["a@example.com"]; a.Weekly == nil || *a.Weekly != 60 {
		t.Fatalf("healthy account not refreshed: %+v", a)
	}
}

func TestLoginUsageFailureWithoutPriorResultCarriesError(t *testing.T) {
	stubLogin(t, []string{"new@example.com"}, func(context.Context, string, string) (SubscriptionQuota, error) {
		return SubscriptionQuota{}, errors.New("no token")
	})
	got := LoginUsage(context.Background(), "codex")
	q, ok := got["new@example.com"]
	if !ok || q.Error == "" || q.FiveHour != nil {
		t.Fatalf("got %+v, want an entry carrying the error", q)
	}
}

func TestLoginUsageOnlyForClaudeAndCodex(t *testing.T) {
	fetches := 0
	stubLogin(t, []string{"x@example.com"}, func(context.Context, string, string) (SubscriptionQuota, error) {
		fetches++
		return SubscriptionQuota{}, nil
	})
	got := LoginUsage(context.Background(), "copilot")
	if got == nil || len(got) != 0 || fetches != 0 {
		t.Fatalf("copilot gave %v with %d fetches, want an empty map and none", got, fetches)
	}
}
