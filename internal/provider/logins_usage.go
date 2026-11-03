package provider

import (
	"context"
	"os"
	"sync"
	"time"
)

// loginUsageTTL is how long LoginUsage reuses its last answer.
const loginUsageTTL = time.Minute

var loginUsageCache struct {
	sync.Mutex
	by map[string]loginUsageEntry // by agent
}

type loginUsageEntry struct {
	at   time.Time
	data map[string]SubscriptionQuota // by email
}

// LoginUsage reports the 5-hour and weekly usage of every remembered account of
// an agent, keyed by email. An answer is reused for a minute. An account whose
// query fails keeps its previous answer; with none yet, it carries the error.
// An agent without subscription usage gives an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	if !knownAgent(agent) {
		return out
	}
	c := &loginUsageCache
	c.Lock()
	prev := c.by[agent]
	c.Unlock()
	if !prev.at.IsZero() && time.Since(prev.at) < loginUsageTTL {
		return copyQuotas(prev.data)
	}
	for _, l := range Logins(agent) {
		// each account has its own time limit, so one slow account cannot
		// starve the others of theirs
		actx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
		q := fetchLoginUsage(actx, l)
		cancel()
		if old, ok := prev.data[l.User]; ok && q.Error != "" {
			q = old
		}
		out[l.User] = q
	}
	c.Lock()
	if c.by == nil {
		c.by = map[string]loginUsageEntry{}
	}
	c.by[agent] = loginUsageEntry{at: time.Now(), data: out}
	c.Unlock()
	return copyQuotas(out)
}

// fetchLoginUsage queries one remembered account with its own token.
func fetchLoginUsage(ctx context.Context, l Login) SubscriptionQuota {
	if l.Agent == "claude" {
		q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: l.Plan, Windows: []QuotaWindow{}}
		var token string
		var err error
		if l.Current {
			token, err = claudeToken(ctx)
		} else {
			token, err = storedClaudeToken(ctx, l.User)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return withWindowShares(claudeUsageWith(ctx, q, token))
	}
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: l.Plan, Windows: []QuotaWindow{}}
	var token, accountID string
	var err error
	if l.Current {
		home, _ := os.UserHomeDir()
		token, accountID, err = codexToken(ctx, codexAuthPath(home))
	} else {
		token, accountID, err = storedCodexToken(ctx, l.User)
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	return withWindowShares(codexUsageWith(ctx, q, token, accountID))
}

// withWindowShares copies the 5-hour and weekly windows into the named fields.
func withWindowShares(q SubscriptionQuota) SubscriptionQuota {
	for _, w := range q.Windows {
		used := w.Used
		switch w.Name {
		case "5 hours":
			q.FiveHourUsed = &used
		case "7 days":
			q.WeeklyUsed = &used
		}
	}
	return q
}

func copyQuotas(in map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// forgetLoginUsage drops the cached answers; tests use it between homes.
func forgetLoginUsage() {
	loginUsageCache.Lock()
	loginUsageCache.by = nil
	loginUsageCache.Unlock()
}
