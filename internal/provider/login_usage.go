package provider

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

// LoginUsage is the allowance used by each remembered account of an agent,
// keyed by the account's email: its 5-hour and weekly windows. A result is
// reused for a minute. An account whose lookup fails keeps what was known of
// it before, so one failing account does not blank the others.

const loginUsageTTL = time.Minute

var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent → user → quota
}

// claudeUsageURL is Claude Code's usage endpoint; a var so tests can point it elsewhere.
var claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// LoginUsage returns the usage of each remembered account of an agent
// ("claude" or "codex"). Other agents, and agents with no accounts, get an
// empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return map[string]SubscriptionQuota{}
	}
	c := &loginUsageCache
	c.Lock()
	if t, ok := c.at[agent]; ok && time.Since(t) < loginUsageTTL {
		out := copyQuotas(c.data[agent])
		c.Unlock()
		return out
	}
	c.Unlock()

	fresh := fetchLoginUsage(ctx, agent)

	c.Lock()
	defer c.Unlock()
	if c.at == nil {
		c.at = map[string]time.Time{}
		c.data = map[string]map[string]SubscriptionQuota{}
	}
	prev := c.data[agent]
	merged := map[string]SubscriptionQuota{}
	for user, q := range fresh {
		if old, ok := prev[user]; ok && q.Error != "" {
			merged[user] = old
			continue
		}
		merged[user] = q
	}
	c.at[agent] = time.Now()
	c.data[agent] = merged
	return copyQuotas(merged)
}

func copyQuotas(in map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// fetchLoginUsage asks every remembered account of an agent for its usage.
func fetchLoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	syncLogins()
	cur, signed := signedInAs(agent, false)
	loginMu.Lock()
	f := loadLogins()
	loginMu.Unlock()
	home, _ := os.UserHomeDir()

	out := map[string]SubscriptionQuota{}
	for _, r := range f.Logins {
		if r.Agent != agent {
			continue
		}
		current := signed && strings.EqualFold(cur.user, r.User)
		q := SubscriptionQuota{Provider: agent, Name: loginName(agent), User: r.User, Plan: r.Plan, Windows: []QuotaWindow{}}
		var err error
		if agent == "claude" {
			q.Icon = "claude-color"
			var tok string
			if current {
				tok, err = claudeToken(ctx)
			} else {
				tok, err = savedClaudeToken(ctx, r.User)
			}
			if err == nil {
				q, err = claudeUsage(ctx, tok, q)
			}
		} else {
			q.Icon = "codex-color"
			var tok, accountID string
			if current {
				tok, accountID, err = codexToken(ctx, codexAuthPath(home))
			} else {
				tok, accountID, err = savedCodexToken(ctx, r.User)
			}
			if err == nil {
				q, err = codexUsage(ctx, tok, accountID, q)
			}
		}
		if err != nil {
			q.Error = err.Error()
		}
		out[r.User] = q
	}
	return out
}

// claudeUsage reads the 5-hour and weekly windows of a Claude account.
func claudeUsage(ctx context.Context, token string, q SubscriptionQuota) (SubscriptionQuota, error) {
	var data struct {
		FiveHour *quotaWire `json:"five_hour"`
		SevenDay *quotaWire `json:"seven_day"`
	}
	err := accountJSON(ctx, claudeUsageURL, token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20", "user-agent": "magpie",
	}, &data)
	if err != nil {
		return q, err
	}
	if w := data.FiveHour; w != nil {
		used := w.Utilization
		q.FiveHour = &used
		q.Windows = append(q.Windows, w.window("5 hours"))
	}
	if w := data.SevenDay; w != nil {
		used := w.Utilization
		q.Weekly = &used
		q.Windows = append(q.Windows, w.window("7 days"))
	}
	return q, nil
}

// codexUsage reads the 5-hour and weekly windows of a ChatGPT account.
func codexUsage(ctx context.Context, token, accountID string, q SubscriptionQuota) (SubscriptionQuota, error) {
	var data struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	base := strings.TrimSuffix(CodexBase, "/codex")
	err := accountJSON(ctx, base+"/wham/usage", token, map[string]string{"chatgpt-account-id": accountID}, &data)
	if err != nil {
		return q, err
	}
	if data.PlanType != "" {
		q.Plan = data.PlanType
	}
	const week, fiveHours = 7 * 24 * 60 * 60, 5 * 60 * 60
	if w := data.RateLimit.Primary; w != nil {
		used := w.UsedPercent
		if w.LimitWindowSecs == week {
			q.Weekly = &used
		} else {
			q.FiveHour = &used
		}
		q.Windows = append(q.Windows, w.window())
	}
	if w := data.RateLimit.Secondary; w != nil {
		used := w.UsedPercent
		if w.LimitWindowSecs == fiveHours {
			q.FiveHour = &used
		} else {
			q.Weekly = &used
		}
		q.Windows = append(q.Windows, w.window())
	}
	return q, nil
}
