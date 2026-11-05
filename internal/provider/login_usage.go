package provider

import (
	"context"
	"strings"
	"sync"
	"time"
)

// loginUsageTTL is how long LoginUsage reuses its last answer for an agent.
const loginUsageTTL = time.Minute

var loginUsageCache = struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent -> account email -> quota
}{at: map[string]time.Time{}, data: map[string]map[string]SubscriptionQuota{}}

// usageAccount is one remembered account and how to ask for its usage.
type usageAccount struct {
	user  string
	quota func(ctx context.Context) SubscriptionQuota
}

// LoginUsage returns the rolling-window usage of every remembered account of
// an agent, keyed by account email. Answers are reused for a minute. When an
// account's query fails, its last good answer is kept. Agents without
// accounts magpie can remember give an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	if checkAgent(agent) != nil {
		return out
	}
	loginUsageCache.Lock()
	if at, ok := loginUsageCache.at[agent]; ok && time.Since(at) < loginUsageTTL {
		for k, v := range loginUsageCache.data[agent] {
			out[k] = v
		}
		loginUsageCache.Unlock()
		return out
	}
	loginUsageCache.Unlock()

	Accounts() // remembers the sign-in in use, so it is listed
	accts := rememberedUsage(agent)
	results := make([]SubscriptionQuota, len(accts))
	var wg sync.WaitGroup
	for i, a := range accts {
		wg.Add(1)
		go func(i int, a usageAccount) {
			defer wg.Done()
			results[i] = a.quota(ctx)
		}(i, a)
	}
	wg.Wait()

	loginUsageCache.Lock()
	defer loginUsageCache.Unlock()
	prev := loginUsageCache.data[agent]
	next := map[string]SubscriptionQuota{}
	for i, a := range accts {
		q := results[i]
		if old, ok := prev[a.user]; ok && q.Error != "" && old.Error == "" {
			q = old // a failed query keeps the last known answer
		}
		next[a.user] = q
	}
	loginUsageCache.data[agent] = next
	loginUsageCache.at[agent] = time.Now()
	for k, v := range next {
		out[k] = v
	}
	return out
}

// rememberedUsage lists the usage sources for an agent: the account signed in
// to, and every other remembered account.
func rememberedUsage(agent string) []usageAccount {
	cur, has := agentLogin(agent)
	accountsMu.Lock()
	ix := loadIndex()
	accountsMu.Unlock()
	var out []usageAccount
	if has {
		out = append(out, liveUsage(agent, cur))
	}
	for _, a := range ix.Accounts {
		if a.Agent != agent || (has && strings.EqualFold(a.User, cur.user)) {
			continue
		}
		out = append(out, storedUsage(a))
	}
	return out
}

func liveUsage(agent string, cur login) usageAccount {
	u := usageAccount{user: cur.user}
	if agent == "claude" {
		u.quota = func(ctx context.Context) SubscriptionQuota {
			return claudeUsage(ctx, claudeToken, func() string { return cur.plan })
		}
		return u
	}
	path := codexAuthPath()
	u.quota = func(ctx context.Context) SubscriptionQuota {
		return codexUsage(ctx, func(ctx context.Context) (string, string, error) {
			return codexToken(ctx, path)
		})
	}
	return u
}

func storedUsage(a rememberedAccount) usageAccount {
	u := usageAccount{user: a.User}
	path := a.path()
	if a.Agent == "claude" {
		u.quota = func(ctx context.Context) SubscriptionQuota {
			return claudeUsage(ctx, func(ctx context.Context) (string, error) {
				return claudeTokenFile(ctx, path)
			}, func() string { return a.Plan })
		}
		return u
	}
	u.quota = func(ctx context.Context) SubscriptionQuota {
		return codexUsage(ctx, func(ctx context.Context) (string, string, error) {
			return codexToken(ctx, path)
		})
	}
	return u
}
