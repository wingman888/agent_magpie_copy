package provider

import (
	"context"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused before the vendors
// are asked again; their private account endpoints rate limit quickly.
const loginUsageTTL = time.Minute

// loginUsageFetcher asks the vendor for one remembered account's usage. It is
// a var so tests can stand in for the vendors. The result carries the windows,
// or Error when the account could not be asked.
var loginUsageFetcher = fetchLoginUsage

// loginUsageAgent is the cached usage of one agent's accounts, keyed by email.
type loginUsageAgent struct {
	at     time.Time
	byUser map[string]SubscriptionQuota
}

var loginUsageCache = struct {
	sync.Mutex
	agents map[string]*loginUsageAgent
}{agents: map[string]*loginUsageAgent{}}

// LoginUsage returns the 5-hour and weekly usage of every remembered account of
// an agent (the agent's current login included), keyed by the account's email.
// Results are reused for a minute. When an account fails to answer, its last
// known result stays in place. An unsupported agent gives an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	id, ok := AccountAgent(agent)
	if !ok {
		return out
	}
	accts := loginUsageAccounts(id)

	c := &loginUsageCache
	c.Lock()
	stale := loginUsageStale(c.agents[id], accts)
	c.Unlock()
	if stale {
		fetched := fetchLoginUsages(ctx, id, accts)
		c.Lock()
		e := c.agents[id]
		if e == nil {
			e = &loginUsageAgent{}
			c.agents[id] = e
		}
		next := make(map[string]SubscriptionQuota, len(accts))
		for _, a := range accts {
			user := a.Account.User
			q := fetched[user]
			if old, ok := e.byUser[user]; ok && q.Error != "" {
				// keep the last known result; the entry is not cleared
				next[user] = old
				continue
			}
			next[user] = q
		}
		e.byUser, e.at = next, time.Now()
		c.Unlock()
	}

	c.Lock()
	defer c.Unlock()
	e := c.agents[id]
	for _, a := range accts {
		if q, ok := e.byUser[a.Account.User]; ok {
			out[a.Account.User] = q
		}
	}
	return out
}

// loginUsageStale reports whether the cached usage for an agent must be fetched
// again: it is older than the TTL, or it does not cover every current account.
// The caller holds the cache lock.
func loginUsageStale(e *loginUsageAgent, accts []Provider) bool {
	if e == nil || time.Since(e.at) >= loginUsageTTL {
		return true
	}
	for _, a := range accts {
		if _, ok := e.byUser[a.Account.User]; !ok {
			return true
		}
	}
	return false
}

// loginUsageAccounts lists the accounts whose usage LoginUsage reports: the
// agent's current login and the remembered ones that hold credentials.
func loginUsageAccounts(agent string) []Provider {
	var out []Provider
	for _, p := range accountProviders(true) {
		if p.Account != nil && p.Account.Agent == agent {
			out = append(out, p)
		}
	}
	return out
}

// fetchLoginUsages asks every account at once and labels each result with its
// agent and email.
func fetchLoginUsages(ctx context.Context, agent string, accts []Provider) map[string]SubscriptionQuota {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	res := make([]SubscriptionQuota, len(accts))
	var wg sync.WaitGroup
	for i, a := range accts {
		wg.Add(1)
		go func(i int, a Provider) {
			defer wg.Done()
			user := a.Account.User
			q := loginUsageFetcher(ctx, agent, user)
			q.Provider, q.Name = agent, user
			if q.Plan == "" {
				q.Plan = a.Account.Plan
			}
			q.fillWindowShares()
			res[i] = q
		}(i, a)
	}
	wg.Wait()
	out := make(map[string]SubscriptionQuota, len(accts))
	for i, a := range accts {
		out[a.Account.User] = res[i]
	}
	return out
}

// fillWindowShares sets FiveHour and Weekly from the windows named by their length.
func (q *SubscriptionQuota) fillWindowShares() {
	for _, w := range q.Windows {
		used := w.Used
		switch w.Name {
		case "5 hours":
			q.FiveHour = &used
		case "7 days":
			q.Weekly = &used
		}
	}
}

// fetchLoginUsage is the real loginUsageFetcher: it signs in with the account's
// own credentials, the agent's file for the current login and magpie's store
// for the others.
func fetchLoginUsage(ctx context.Context, agent, user string) SubscriptionQuota {
	cur, hasCur := currentSignIn(agent)
	current := hasCur && sameUser(cur.Account.User, user)
	switch agent {
	case "claude":
		src := storedClaudeSource(user)
		if current {
			src = claudeAgentSource()
		}
		return claudeLoginUsage(ctx, src)
	case "codex":
		src := storedCodexSource(user)
		if current {
			src = codexFileSource(codexAuthPath())
		}
		return codexLoginUsage(ctx, src)
	}
	return SubscriptionQuota{Error: "usage is not available for " + agent}
}

// claudeLoginUsage reads the windows of the Claude account whose tokens src holds.
func claudeLoginUsage(ctx context.Context, src claudeSource) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "claude", Icon: "claude-color", Windows: []QuotaWindow{}}
	token, err := claudeTokenFrom(ctx, src)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	wins, err := claudeWindows(ctx, token)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Windows = append(q.Windows, wins...)
	return q
}

// codexLoginUsage reads the plan and windows of the Codex account whose tokens src holds.
func codexLoginUsage(ctx context.Context, src codexSource) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "codex", Icon: "codex-color", Windows: []QuotaWindow{}}
	token, accountID, err := codexTokenFrom(ctx, src)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	plan, wins, err := codexWindows(ctx, token, accountID)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan = plan
	q.Windows = append(q.Windows, wins...)
	return q
}
