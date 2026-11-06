package provider

// Per-login usage: how much of the 5-hour and weekly allowance each remembered
// subscription login has used. Results are kept for a minute; when one login
// cannot be asked, its last known result stands.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused.
const loginUsageTTL = time.Minute

// usageFetch asks the vendor about one login. A var so tests never reach the network.
var usageFetch = fetchLoginUsage

var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time                    // by agent
	data map[string]map[string]SubscriptionQuota // by agent, then lower-case email
}

// LoginUsage returns the usage of every remembered login of an agent, keyed by
// the account's lower-case email. It is empty for agents without subscriptions
// magpie can read, and for names it does not know.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	name, ok := LoginAgent(agent)
	if !ok {
		return out
	}
	c := &loginUsageCache
	// The lock covers the cache only: the vendor calls below run without it,
	// so a slow account never holds up other readers.
	c.Lock()
	if c.data == nil {
		c.data, c.at = map[string]map[string]SubscriptionQuota{}, map[string]time.Time{}
	}
	if time.Since(c.at[name]) < loginUsageTTL {
		out := copyQuotas(c.data[name])
		c.Unlock()
		return out
	}
	known := copyQuotas(c.data[name])
	c.Unlock()

	res := map[string]SubscriptionQuota{}
	for _, l := range listLogins(name) {
		key := strings.ToLower(l.User)
		// each account gets its own time limit, so one slow account cannot
		// use up the time of the others
		actx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
		q, err := usageFetch(actx, l)
		cancel()
		if err != nil {
			if prev, ok := known[key]; ok {
				res[key] = prev
				continue
			}
			q = SubscriptionQuota{Provider: name, Name: agentDisplayName(name), Plan: l.Plan, Windows: []QuotaWindow{}, Error: err.Error()}
		}
		res[key] = q
	}

	c.Lock()
	c.data[name], c.at[name] = res, time.Now()
	c.Unlock()
	return copyQuotas(res)
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func agentDisplayName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// fetchLoginUsage asks the vendor's usage endpoint with the login's own token.
func fetchLoginUsage(ctx context.Context, l savedLogin) (SubscriptionQuota, error) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	q := SubscriptionQuota{Provider: l.Agent, Name: agentDisplayName(l.Agent), Plan: l.Plan, Windows: []QuotaWindow{}}
	switch l.Agent {
	case "claude":
		var tok string
		var err error
		if l.current {
			tok, err = claudeToken(ctx)
		} else {
			tok, err = storedClaudeToken(ctx, l.User)
		}
		if err != nil {
			return q, err
		}
		var data struct {
			FiveHour *quotaWire `json:"five_hour"`
			SevenDay *quotaWire `json:"seven_day"`
		}
		err = accountJSON(ctx, "https://api.anthropic.com/api/oauth/usage", tok, map[string]string{
			"anthropic-beta": "oauth-2025-04-20", "user-agent": "magpie",
		}, &data)
		if err != nil {
			return q, err
		}
		if data.FiveHour != nil {
			q.Windows = append(q.Windows, data.FiveHour.window("5 hours"))
		}
		if data.SevenDay != nil {
			q.Windows = append(q.Windows, data.SevenDay.window("7 days"))
		}
	case "codex":
		var tok, accountID string
		var err error
		if l.current {
			home, _ := os.UserHomeDir()
			tok, accountID, err = codexToken(ctx, codexAuthFile(home))
		} else {
			tok, accountID, err = storedCodexToken(ctx, l.User)
		}
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
		base := strings.TrimSuffix(CodexBase, "/codex")
		err = accountJSON(ctx, base+"/wham/usage", tok, map[string]string{"chatgpt-account-id": accountID}, &data)
		if err != nil {
			return q, err
		}
		if data.PlanType != "" {
			q.Plan = data.PlanType
		}
		if data.RateLimit.Primary != nil {
			q.Windows = append(q.Windows, data.RateLimit.Primary.window())
		}
		if data.RateLimit.Secondary != nil {
			q.Windows = append(q.Windows, data.RateLimit.Secondary.window())
		}
	}
	for _, w := range q.Windows {
		switch w.Name {
		case "5 hours":
			q.FiveHour = w.Used
		case "7 days":
			q.Weekly = w.Used
		}
	}
	return q, nil
}
