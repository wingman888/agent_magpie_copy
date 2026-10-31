package provider

import (
	"context"
	"strings"
	"sync"
	"time"
)

var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent → email → quota
}

// loginUsageNow is the clock the one-minute reuse is measured with.
var loginUsageNow = time.Now

// LoginUsage reports, per remembered account (by email), how much of the
// 5-hour and weekly allowance is used. Answers are reused for a minute; an
// account whose query fails keeps what was last known about it. Agents
// without switchable accounts yield nothing.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	a, err := LoginAgent(agent)
	if err != nil {
		return out
	}
	logins, _ := Logins(a)

	c := &loginUsageCache
	c.Lock()
	defer c.Unlock() // one refresh at a time; later callers reuse its result
	if c.at == nil {
		c.at, c.data = map[string]time.Time{}, map[string]map[string]SubscriptionQuota{}
	}
	prev := c.data[a]
	if t, ok := c.at[a]; ok && loginUsageNow().Sub(t) < time.Minute {
		all := true
		for _, l := range logins {
			if _, ok := prev[l.User]; !ok && (l.Current || l.Saved) {
				all = false
			}
		}
		if all {
			return copyQuotas(prev, logins)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	type result struct {
		user string
		q    SubscriptionQuota
	}
	ch := make(chan result, len(logins))
	n := 0
	for _, l := range logins {
		if !l.Current && !l.Saved {
			continue
		}
		n++
		go func() { ch <- result{l.User, fetchLoginUsage(ctx, l)} }()
	}
	fresh := map[string]SubscriptionQuota{}
	for i := 0; i < n; i++ {
		r := <-ch
		if old, ok := prev[r.user]; ok && r.q.Error != "" && old.Error == "" {
			r.q = old // keep what was known
		}
		fresh[r.user] = r.q
	}
	c.data[a], c.at[a] = fresh, loginUsageNow()
	return copyQuotas(fresh, logins)
}

func copyQuotas(m map[string]SubscriptionQuota, logins []Login) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	for _, l := range logins {
		if q, ok := m[l.User]; ok {
			out[l.User] = q
		}
	}
	return out
}

func fetchLoginUsage(ctx context.Context, l Login) SubscriptionQuota {
	user := l.User
	switch l.Agent {
	case "claude":
		return claudeUsage(ctx, func(ctx context.Context) (string, error) {
			if cur, ok := currentLogin("claude", false); ok && strings.EqualFold(cur.user, user) {
				return claudeToken(ctx)
			}
			return savedClaudeToken(ctx, user)
		}, l.Plan)
	case "codex":
		return codexUsage(ctx, func(ctx context.Context) (string, string, error) {
			if cur, ok := currentLogin("codex", false); ok && strings.EqualFold(cur.user, user) {
				return codexToken(ctx, codexAuthPath())
			}
			return savedCodexToken(ctx, user)
		})
	}
	return SubscriptionQuota{Error: "unsupported agent"}
}
