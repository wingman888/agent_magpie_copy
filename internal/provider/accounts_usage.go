package provider

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// loginUsageTTL is how long LoginUsage reuses what it last fetched.
const loginUsageTTL = time.Minute

// loginAccount is one account LoginUsage reports on.
type loginAccount struct {
	Agent   string
	User    string
	Plan    string
	Dir     string // the account's folder; "" for the one the agent is signed in to
	Current bool
}

// usageFetch asks the vendor for one account's usage. A var so tests can feed
// fixed answers.
var usageFetch = func(ctx context.Context, a loginAccount) SubscriptionQuota {
	switch a.Agent {
	case "claude":
		if a.Current {
			return claudeQuota(ctx, claudeToken)
		}
		return claudeQuota(ctx, func(ctx context.Context) (string, error) {
			return storedClaudeToken(ctx, filepath.Join(a.Dir, ".credentials.json"))
		})
	case "codex":
		path := codexAuthFile()
		if !a.Current {
			path = filepath.Join(a.Dir, "auth.json")
		}
		return codexSubscriptionUsage(ctx, path)
	}
	return SubscriptionQuota{}
}

// loginUsageCache keeps the last LoginUsage answer per agent.
var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent → account email → usage
}

// resetLoginUsage drops the cached answers; tests use it between homes.
func resetLoginUsage() {
	loginUsageCache.Lock()
	loginUsageCache.at, loginUsageCache.data = nil, nil
	loginUsageCache.Unlock()
}

// loginAccounts lists the agent's accounts: the one signed in first, then the
// remembered ones in the order they were saved.
func loginAccounts(agent string) []loginAccount {
	var out []loginAccount
	cur, signedIn := currentLogin(agent)
	if signedIn {
		out = append(out, loginAccount{Agent: agent, User: cur.Account.User, Plan: cur.Account.Plan, Current: true})
	}
	storeMu.Lock()
	ix := loadIndex()
	storeMu.Unlock()
	for _, s := range ix.Accounts {
		if s.Agent != agent || (signedIn && strings.EqualFold(s.User, cur.Account.User)) {
			continue
		}
		out = append(out, loginAccount{Agent: agent, User: s.User, Plan: s.Plan, Dir: accountFolder(agent, s.User)})
	}
	return out
}

// LoginUsage reports the rolling usage of each account of an agent, keyed by
// account email: the share of the 5-hour and the weekly window used. Answers
// are reused for a minute. An account whose query fails keeps what was last
// known of it. An agent without such accounts gets an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	a, ok := AccountAgent(agent)
	if !ok {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsageCache
	c.Lock()
	if t, ok := c.at[a]; ok && time.Since(t) < loginUsageTTL {
		out := copyQuotas(c.data[a])
		c.Unlock()
		return out
	}
	c.Unlock()

	accts := loginAccounts(a)
	fresh := make(map[string]SubscriptionQuota, len(accts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, acct := range accts {
		wg.Add(1)
		go func(acct loginAccount) {
			defer wg.Done()
			q := usageFetch(ctx, acct)
			if q.Plan == "" {
				q.Plan = acct.Plan
			}
			fillQuotaWindows(&q)
			mu.Lock()
			fresh[acct.User] = q
			mu.Unlock()
		}(acct)
	}
	wg.Wait()

	c.Lock()
	defer c.Unlock()
	if c.at == nil {
		c.at = map[string]time.Time{}
		c.data = map[string]map[string]SubscriptionQuota{}
	}
	prev := c.data[a]
	out := make(map[string]SubscriptionQuota, len(fresh))
	for user, q := range fresh {
		if old, ok := prev[user]; ok && q.Error != "" && old.Error == "" {
			out[user] = old // a failed query keeps what was known before
			continue
		}
		out[user] = q
	}
	c.at[a] = time.Now()
	c.data[a] = out
	return copyQuotas(out)
}

// fillQuotaWindows sets FiveHour and Weekly from the windows of those names.
func fillQuotaWindows(q *SubscriptionQuota) {
	for _, w := range q.Windows {
		switch w.Name {
		case "5 hours":
			q.FiveHour = w.Used
		case "7 days":
			q.Weekly = w.Used
		}
	}
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
