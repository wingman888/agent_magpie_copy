package provider

// Per-account quota for the saved Claude Code and Codex logins: each login's
// 5-hour and weekly used share, keyed by the login's email. Kept apart from
// SubscriptionUsage, which reports one entry per agent.

import (
	"context"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused.
const loginUsageTTL = time.Minute

// claudeUsageURL and codexUsageURL are the usage endpoints; vars so tests can
// point them at a local server.
var (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
)

type loginUsageEntry struct {
	at   time.Time
	data map[string]SubscriptionQuota
}

var loginUsageCache = struct {
	sync.Mutex
	byAgent map[string]loginUsageEntry
}{byAgent: map[string]loginUsageEntry{}}

// LoginUsage returns the 5-hour and weekly usage of each saved login of agent
// ("claude" or "codex"), keyed by email. Results are reused for a minute. When
// one account cannot be read, its earlier result is kept; with no earlier
// result it comes back with Error set. An unsupported agent gives an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	a, ok := LoginAgent(agent)
	if !ok {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsageCache
	c.Lock()
	e, have := c.byAgent[a]
	c.Unlock()
	if have && time.Since(e.at) < loginUsageTTL {
		return copyQuotas(e.data)
	}
	fresh := fetchLoginUsage(ctx, a, e.data)
	c.Lock()
	c.byAgent[a] = loginUsageEntry{at: time.Now(), data: fresh}
	c.Unlock()
	return copyQuotas(fresh)
}

// fetchLoginUsage asks every saved login of agent at once. prev holds the last
// known results and is used for any account that fails now.
func fetchLoginUsage(ctx context.Context, agent string, prev map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	logins := Logins(agent)
	quotas := make([]SubscriptionQuota, len(logins))
	errs := make([]error, len(logins))
	var wg sync.WaitGroup
	for i, l := range logins {
		wg.Add(1)
		go func(i int, l Login) {
			defer wg.Done()
			quotas[i], errs[i] = loginUsageOne(ctx, l)
		}(i, l)
	}
	wg.Wait()
	out := make(map[string]SubscriptionQuota, len(logins))
	for i, l := range logins {
		if errs[i] != nil {
			if old, ok := prev[l.User]; ok {
				out[l.User] = old
				continue
			}
			failed := quotas[i]
			failed.Error = errs[i].Error()
			out[l.User] = failed
			continue
		}
		out[l.User] = quotas[i]
	}
	return out
}

func loginUsageOne(ctx context.Context, l Login) (SubscriptionQuota, error) {
	if l.Agent == "claude" {
		return claudeLoginUsage(ctx, l)
	}
	return codexLoginUsage(ctx, l)
}

// claudeLoginUsage reads the usage of a Claude Code login. The agent's own
// login is read from its store; a saved one from magpie's copy.
func claudeLoginUsage(ctx context.Context, l Login) (SubscriptionQuota, error) {
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: l.Plan, Windows: []QuotaWindow{}}
	dir := ""
	if !l.Current {
		dir = loginDir("claude", l.User)
	}
	token, err := claudeTokenAt(ctx, dir)
	if err != nil {
		return q, err
	}
	var data struct {
		FiveHour *quotaWire `json:"five_hour"`
		SevenDay *quotaWire `json:"seven_day"`
	}
	err = accountJSON(ctx, claudeUsageURL, token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20", "user-agent": "magpie",
	}, &data)
	if err != nil {
		return q, err
	}
	if data.FiveHour != nil {
		q.FiveHour = data.FiveHour.Utilization
		q.Windows = append(q.Windows, data.FiveHour.window("5 hours"))
	}
	if data.SevenDay != nil {
		q.Weekly = data.SevenDay.Utilization
		q.Windows = append(q.Windows, data.SevenDay.window("7 days"))
	}
	return q, nil
}

// codexLoginUsage reads the usage of a Codex login. The agent's own login is
// read from ~/.codex/auth.json; a saved one from magpie's copy.
func codexLoginUsage(ctx context.Context, l Login) (SubscriptionQuota, error) {
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: l.Plan, Windows: []QuotaWindow{}}
	path := codexAuthPath()
	if !l.Current {
		path = loginTokens("codex", l.User)
	}
	token, accountID, err := codexToken(ctx, path)
	if err != nil {
		return q, err
	}
	var data struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	headers := map[string]string{}
	if accountID != "" {
		headers["chatgpt-account-id"] = accountID
	}
	if err := accountJSON(ctx, codexUsageURL, token, headers, &data); err != nil {
		return q, err
	}
	if data.PlanType != "" {
		q.Plan = data.PlanType
	}
	// A window says its length in seconds; when it does not, the primary one is
	// the 5-hour window and the secondary one the weekly.
	for pos, w := range []*codexWindow{data.RateLimit.Primary, data.RateLimit.Secondary} {
		if w == nil {
			continue
		}
		q.Windows = append(q.Windows, w.window())
		weekly := pos == 1
		switch w.LimitWindowSecs {
		case 5 * 60 * 60:
			weekly = false
		case 7 * 24 * 60 * 60:
			weekly = true
		}
		if weekly {
			q.Weekly = w.UsedPercent
		} else {
			q.FiveHour = w.UsedPercent
		}
	}
	return q, nil
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
