package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// loginUsageTTL is how long one agent's per-account quotas are reused.
const loginUsageTTL = time.Minute

// loginUsage caches each agent's quota per remembered account, by email.
var loginUsage struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota
}

// LoginUsage returns the rolling 5-hour and weekly usage of every remembered
// sign-in of an agent (claude or codex), keyed by email. Results are reused for
// a minute. An account whose query fails keeps its last known result. An agent
// magpie does not track returns an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	id, ok := AgentID(agent)
	if !ok {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsage
	c.Lock()
	if t, ok := c.at[id]; ok && time.Since(t) < loginUsageTTL {
		out := cloneQuotas(c.data[id])
		c.Unlock()
		return out
	}
	prev := cloneQuotas(c.data[id])
	c.Unlock()

	fresh := fetchLoginUsage(ctx, id, prev)

	c.Lock()
	defer c.Unlock()
	if c.at == nil {
		c.at = map[string]time.Time{}
		c.data = map[string]map[string]SubscriptionQuota{}
	}
	c.at[id] = time.Now()
	c.data[id] = fresh
	return cloneQuotas(fresh)
}

// fetchLoginUsage asks every remembered account of one agent at once.
func fetchLoginUsage(ctx context.Context, agent string, prev map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()

	type job struct {
		user  string
		fetch func() SubscriptionQuota
	}
	var jobs []job
	accountsMu.Lock()
	f := loadAccounts()
	accountsMu.Unlock()
	cur, _, signedIn := currentLogin(agent)
	for _, a := range f.Accounts {
		if a.Agent != agent {
			continue
		}
		user, plan := a.User, a.Plan
		isCur := signedIn && strings.EqualFold(cur, user)
		if !isCur && a.Creds == nil {
			continue // magpie holds no tokens for it, so it cannot ask
		}
		switch {
		case isCur && agent == "claude":
			jobs = append(jobs, job{user, func() SubscriptionQuota { return claudeSubscriptionUsage(ctx) }})
		case isCur && agent == "codex":
			home, _ := os.UserHomeDir()
			path := filepath.Join(home, ".codex", "auth.json")
			jobs = append(jobs, job{user, func() SubscriptionQuota { return codexSubscriptionUsage(ctx, path) }})
		case agent == "claude":
			jobs = append(jobs, job{user, func() SubscriptionQuota {
				q := quotaShell(agent)
				q.Plan = plan
				tok, err := storedClaudeToken(ctx, user)
				if err != nil {
					q.Error = err.Error()
					return q
				}
				return claudeQuota(ctx, q, tok)
			}})
		default:
			jobs = append(jobs, job{user, func() SubscriptionQuota {
				q := quotaShell(agent)
				q.Plan = plan
				tok, acct, err := storedCodexToken(ctx, user)
				if err != nil {
					q.Error = err.Error()
					return q
				}
				return codexQuota(ctx, q, tok, acct)
			}})
		}
	}

	res := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) { defer wg.Done(); res[i] = j.fetch() }(i, j)
	}
	wg.Wait()

	out := map[string]SubscriptionQuota{}
	for i, j := range jobs {
		q := res[i]
		if old, ok := prev[j.user]; ok && q.Error != "" && old.Error == "" {
			out[j.user] = old // a failed query keeps what was known before
			continue
		}
		out[j.user] = q
	}
	return out
}

// quotaShell is an empty quota for an agent, to be filled in.
func quotaShell(agent string) SubscriptionQuota {
	if agent == "claude" {
		return SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Windows: []QuotaWindow{}}
	}
	return SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Windows: []QuotaWindow{}}
}

func cloneQuotas(in map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
