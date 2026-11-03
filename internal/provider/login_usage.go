package provider

import (
	"context"
	"sync"
	"time"
)

// loginUsageTTL is how long LoginUsage reuses a result. The vendors' usage
// endpoints are rate limited, so one `magpie accounts` run must not hit them
// once per call.
const loginUsageTTL = time.Minute

// loginUsageState is the cached result for one agent.
type loginUsageState struct {
	at   time.Time                    // when data was last fetched in full; zero if never
	data map[string]SubscriptionQuota // keyed by sign-in email
	busy chan struct{}                // closed when the fetch in flight is done
}

var loginUsageCache struct {
	sync.Mutex
	byAgent map[string]*loginUsageState
}

// LoginUsage returns the rolling quota usage of every remembered sign-in of an
// agent (claude or codex; cc is an alias), keyed by email. The sign-in the agent
// is using reads its tokens from the agent's own files; the others read magpie's
// store. Each account is asked on its own. An account whose lookup fails keeps
// whatever result it had before, so a flaky endpoint does not blank it out.
// Results are reused for a minute. An unsupported agent gives an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	agent, err := AccountAgent(agent)
	if err != nil {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsageCache
	for {
		c.Lock()
		if c.byAgent == nil {
			c.byAgent = map[string]*loginUsageState{}
		}
		st := c.byAgent[agent]
		if st == nil {
			st = &loginUsageState{}
			c.byAgent[agent] = st
		}
		fresh := !st.at.IsZero() && time.Since(st.at) < loginUsageTTL
		switch {
		case fresh || ctx.Err() != nil:
			out := cloneLoginUsage(st.data)
			c.Unlock()
			return out
		case st.busy != nil:
			// another caller is already fetching this agent; wait for it
			wait := st.busy
			c.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				c.Lock()
				out := cloneLoginUsage(st.data)
				c.Unlock()
				return out
			}
		}
		done := make(chan struct{})
		st.busy = done
		prior := st.data
		c.Unlock()

		out := fetchLoginUsage(ctx, agent, prior)

		c.Lock()
		st.data, st.busy = out, nil
		if ctx.Err() == nil {
			st.at = time.Now()
		}
		close(done)
		c.Unlock()
		return cloneLoginUsage(out)
	}
}

// fetchLoginUsage asks every remembered sign-in of agent about its own quota.
// prior is the last known result; a sign-in that fails keeps its entry from it.
func fetchLoginUsage(ctx context.Context, agent string, prior map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	rememberCurrent(agent)
	cur, _, hasCur := currentSignIn(agent)
	accountsMu.Lock()
	f := loadAccounts()
	accountsMu.Unlock()

	type job struct {
		user, plan string
		current    bool
	}
	var jobs []job
	for _, s := range f.Accounts {
		if s.Agent != agent {
			continue
		}
		jobs = append(jobs, job{user: s.User, plan: s.Plan, current: hasCur && sameUser(cur, s.User)})
	}

	res := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			res[i] = accountLoginUsage(ctx, agent, j.user, j.plan, j.current)
		}(i, j)
	}
	wg.Wait()

	out := make(map[string]SubscriptionQuota, len(jobs))
	for i, j := range jobs {
		q := res[i]
		if q.Error != "" {
			if p, ok := prior[j.user]; ok {
				out[j.user] = p
				continue
			}
		}
		out[j.user] = q
	}
	return out
}

// accountLoginUsage asks one sign-in for its windows, with its own credentials.
func accountLoginUsage(ctx context.Context, agent, user, plan string, current bool) SubscriptionQuota {
	switch agent {
	case "claude":
		q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: plan, Windows: []QuotaWindow{}}
		var tok string
		var err error
		if current {
			tok, err = claudeToken(ctx)
		} else {
			tok, err = savedClaudeToken(ctx, user)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return claudeWindows(ctx, tok, q)
	case "codex":
		q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: plan, Windows: []QuotaWindow{}}
		var tok, accountID string
		var err error
		if current {
			tok, accountID, err = codexToken(ctx, codexAuthPath())
		} else {
			tok, accountID, err = savedCodexToken(ctx, user)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		q = codexWindows(ctx, tok, accountID, q)
		if q.Plan == "" {
			q.Plan = plan
		}
		return q
	}
	return SubscriptionQuota{Error: "no usage for " + agent}
}

// cloneLoginUsage copies a result so callers cannot change the cache.
func cloneLoginUsage(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, q := range m {
		q.Windows = append([]QuotaWindow{}, q.Windows...)
		out[k] = q
	}
	return out
}
