package provider

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// luSetup signs Codex in as me@example.com, remembers a second account
// other@example.com with credentials, and swaps in fetch for the vendor call.
// It returns a counter of the calls made to the fetcher.
func luSetup(t *testing.T, fetch func(user string) SubscriptionQuota) *luCounter {
	t.Helper()
	signIn(t)
	f := loadAccounts()
	f.Accounts = append(f.Accounts, savedAccount{Agent: "codex", User: "Other@Example.com", Plan: "plus", Cred: json.RawMessage(`{"tokens":{}}`)})
	if err := storeAccounts(f); err != nil {
		t.Fatal(err)
	}
	c := &luCounter{fetch: fetch}
	old := loginUsageFetcher
	loginUsageFetcher = func(_ context.Context, agent, user string) SubscriptionQuota {
		c.mu.Lock()
		c.calls++
		c.mu.Unlock()
		return c.fetch(user)
	}
	luResetCache()
	t.Cleanup(func() {
		loginUsageFetcher = old
		luResetCache()
	})
	return c
}

func luResetCache() {
	c := &loginUsageCache
	c.Lock()
	c.agents = map[string]*loginUsageAgent{}
	c.Unlock()
}

// luAge makes the cached usage of an agent look older than the reuse window.
func luAge(agent string) {
	c := &loginUsageCache
	c.Lock()
	defer c.Unlock()
	if e := c.agents[agent]; e != nil {
		e.at = time.Now().Add(-2 * loginUsageTTL)
	}
}

type luCounter struct {
	mu    sync.Mutex
	calls int
	fetch func(user string) SubscriptionQuota
}

func (c *luCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// luOK is a successful answer with a 5-hour and a weekly window.
func luOK(user string, five, week float64) SubscriptionQuota {
	return SubscriptionQuota{Provider: "codex", Name: user, Windows: []QuotaWindow{
		{Name: "5 hours", Used: five}, {Name: "7 days", Used: week},
	}}
}

func TestLuEachAccountReturnsItsOwnUsage(t *testing.T) {
	luSetup(t, func(user string) SubscriptionQuota {
		if user == "me@example.com" {
			return luOK(user, 10, 20)
		}
		return luOK(user, 55, 60)
	})
	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2: %+v", len(got), got)
	}
	me, other := got["me@example.com"], got["Other@Example.com"]
	if me.FiveHour == nil || *me.FiveHour != 10 || me.Weekly == nil || *me.Weekly != 20 {
		t.Errorf("me = %+v, want 5h 10 and weekly 20", me)
	}
	if other.FiveHour == nil || *other.FiveHour != 55 || other.Weekly == nil || *other.Weekly != 60 {
		t.Errorf("other = %+v, want 5h 55 and weekly 60", other)
	}
	if me.Provider != "codex" || other.Name != "Other@Example.com" {
		t.Errorf("labels wrong: me=%+v other=%+v", me, other)
	}
}

func TestLuReusesResultWithinAMinute(t *testing.T) {
	c := luSetup(t, func(user string) SubscriptionQuota { return luOK(user, 1, 2) })
	LoginUsage(context.Background(), "codex")
	if n := c.count(); n != 2 {
		t.Fatalf("first call made %d fetches, want 2", n)
	}
	LoginUsage(context.Background(), "codex")
	if n := c.count(); n != 2 {
		t.Fatalf("call within a minute fetched again: %d fetches", n)
	}
	luAge("codex")
	LoginUsage(context.Background(), "codex")
	if n := c.count(); n != 4 {
		t.Fatalf("call after a minute made %d fetches total, want 4", n)
	}
}

func TestLuFailedAccountKeepsPreviousResult(t *testing.T) {
	fail := false
	luSetup(t, func(user string) SubscriptionQuota {
		if fail && user == "Other@Example.com" {
			return SubscriptionQuota{Error: "Too Many Requests"}
		}
		return luOK(user, 55, 60)
	})
	LoginUsage(context.Background(), "codex")
	fail = true
	luAge("codex")
	got := LoginUsage(context.Background(), "codex")
	other, ok := got["Other@Example.com"]
	if !ok || other.Error != "" || other.FiveHour == nil || *other.FiveHour != 55 {
		t.Fatalf("failed account lost its last known result: %+v (present=%v)", other, ok)
	}
	if me := got["me@example.com"]; me.FiveHour == nil || *me.FiveHour != 55 {
		t.Fatalf("healthy account not refreshed: %+v", me)
	}
}

func TestLuFailureWithoutPreviousResultReportsError(t *testing.T) {
	luSetup(t, func(user string) SubscriptionQuota {
		if user == "Other@Example.com" {
			return SubscriptionQuota{Error: "network down"}
		}
		return luOK(user, 3, 4)
	})
	got := LoginUsage(context.Background(), "codex")
	if other := got["Other@Example.com"]; other.Error != "network down" {
		t.Fatalf("expected the error to be reported, got %+v", other)
	}
}

func TestLuUnsupportedAgentIsEmpty(t *testing.T) {
	c := luSetup(t, func(user string) SubscriptionQuota { return luOK(user, 1, 2) })
	for _, agent := range []string{"copilot", "gemini", ""} {
		if got := LoginUsage(context.Background(), agent); got == nil || len(got) != 0 {
			t.Errorf("LoginUsage(%q) = %+v, want an empty map", agent, got)
		}
	}
	if n := c.count(); n != 0 {
		t.Fatalf("unsupported agents triggered %d fetches", n)
	}
}
