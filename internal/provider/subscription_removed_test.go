package provider

import (
	"context"
	"testing"
	"time"
)

// subUsageResetCache forces SubscriptionUsage to recompute and drops the
// result afterwards so later tests do not see it.
func subUsageResetCache(t *testing.T) {
	t.Helper()
	reset := func() {
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.at = time.Time{}
		subscriptionUsageCache.data = nil
		subscriptionUsageCache.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// subUsageOffline is a cancelled context: no network is touched, and any
// account that is still listed shows up with an error entry.
func subUsageOffline() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestSubUsageRemovedCodexNotListed(t *testing.T) {
	signIn(t)
	subUsageResetCache(t)

	// positive control: codex is listed before it is removed
	hasCodex := func() bool {
		for _, q := range SubscriptionUsage(subUsageOffline()) {
			if q.Provider == "codex" {
				return true
			}
		}
		return false
	}
	if !hasCodex() {
		t.Fatal("codex not in subscription usage before removal")
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	subUsageResetCache(t)
	if hasCodex() {
		t.Fatal("removed codex still counted in subscription usage")
	}
}
