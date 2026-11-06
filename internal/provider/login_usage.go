package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused before the vendors
// are asked again; their usage endpoints are rate limited.
const loginUsageTTL = time.Minute

// loginUsageClock is the clock the TTL is measured on; tests replace it.
var loginUsageClock = time.Now

// loginUsers lists the emails of the accounts signed in to an agent. It is a
// variable so tests can name accounts without a real sign-in.
var loginUsers = func(agent string) []string {
	var out []string
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == agent && p.Account.User != "" {
			out = append(out, p.Account.User)
		}
	}
	return out
}

// usageFetch reads one account's quota from its vendor. It is a variable so
// tests can stand in for the network.
var usageFetch = func(ctx context.Context, agent, user string) (SubscriptionQuota, error) {
	var q SubscriptionQuota
	switch agent {
	case "claude":
		q = claudeSubscriptionUsage(ctx)
	case "codex":
		home, _ := os.UserHomeDir()
		q = codexSubscriptionUsage(ctx, filepath.Join(home, ".codex", "auth.json"))
	default:
		return q, errors.New("no usage for " + agent)
	}
	if q.Error != "" {
		return q, errors.New(q.Error)
	}
	return q, nil
}

// loginUsageCache holds the last LoginUsage result per agent. A stored map is
// never changed after it is stored, so readers may keep using it unlocked.
var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent -> email -> quota
}

// LoginUsage returns the 5-hour and weekly used shares of each account signed
// in to an agent, keyed by email. Results are reused for a minute. An account
// whose query fails keeps its previously known result; with none, its entry
// carries the error. Agents without usage reports give an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	if agent != "claude" && agent != "codex" {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsageCache
	c.Lock()
	cached, ok := c.data[agent]
	fresh := ok && loginUsageClock().Sub(c.at[agent]) < loginUsageTTL
	c.Unlock()
	if fresh {
		return copyQuotas(cached)
	}

	cur := map[string]SubscriptionQuota{}
	for _, user := range loginUsers(agent) {
		q, err := usageFetch(ctx, agent, user)
		if err != nil {
			if old, known := cached[user]; known {
				cur[user] = old
				continue
			}
			cur[user] = SubscriptionQuota{Provider: agent, Name: q.Name, Icon: q.Icon, Windows: []QuotaWindow{}, Error: err.Error()}
			continue
		}
		q.FiveHour, q.Weekly = windowShares(q)
		cur[user] = q
	}

	c.Lock()
	if c.data == nil {
		c.data, c.at = map[string]map[string]SubscriptionQuota{}, map[string]time.Time{}
	}
	c.data[agent], c.at[agent] = cur, loginUsageClock()
	c.Unlock()
	return copyQuotas(cur)
}

// windowShares finds the 5-hour and 7-day windows among a quota's windows.
func windowShares(q SubscriptionQuota) (fiveHour, weekly *float64) {
	for _, w := range q.Windows {
		v := w.Used
		switch w.Name {
		case "5 hours":
			fiveHour = &v
		case "7 days":
			weekly = &v
		}
	}
	return fiveHour, weekly
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
