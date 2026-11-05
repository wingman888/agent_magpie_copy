package provider

import (
	"context"
	"os"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused; the vendors'
// private usage endpoints are rate limited.
const loginUsageTTL = time.Minute

// loginUsage keeps, per agent, the last LoginUsage result and when it was made.
var loginUsage = struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota
}{at: map[string]time.Time{}, data: map[string]map[string]SubscriptionQuota{}}

// LoginUsage returns each remembered login's rolling usage for an agent, keyed
// by the account's email: the 5-hour and weekly shares are in FiveHourUsed and
// WeeklyUsed. A result is reused for a minute. When a login cannot be read
// now, its last known usage is kept and the failure is noted in Error. An
// agent without subscription usage gets an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	if checkAgent(agent) != nil {
		return out
	}
	loginUsage.Lock()
	defer loginUsage.Unlock()

	if prev, ok := loginUsage.data[agent]; ok && time.Since(loginUsage.at[agent]) < loginUsageTTL {
		for k, v := range prev {
			out[k] = v
		}
		return out
	}
	prev := loginUsage.data[agent]
	logins, _ := Logins(agent)
	for _, l := range logins {
		q := loginQuota(ctx, l)
		if q.Error != "" {
			if old, ok := prev[l.User]; ok && len(old.Windows) > 0 {
				old.Error = q.Error
				q = old
			}
		}
		for _, w := range q.Windows {
			switch w.Name {
			case "5 hours":
				q.FiveHourUsed = w.Used
			case "7 days":
				q.WeeklyUsed = w.Used
			}
		}
		out[l.User] = q
	}
	loginUsage.at[agent], loginUsage.data[agent] = time.Now(), out
	copied := make(map[string]SubscriptionQuota, len(out))
	for k, v := range out {
		copied[k] = v
	}
	return copied
}

// loginQuota reads one login's usage with the credentials that login holds.
func loginQuota(ctx context.Context, l Login) SubscriptionQuota {
	switch l.Agent {
	case "claude":
		var tok string
		var err error
		if l.Current {
			tok, err = claudeToken(ctx)
		} else {
			var oauth map[string]any
			oauth, err = savedClaudeOAuth(ctx, l.User)
			tok, _ = oauth["accessToken"].(string)
		}
		if err != nil {
			return SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: l.Plan, Windows: []QuotaWindow{}, Error: err.Error()}
		}
		return claudeQuotaWith(ctx, tok, l.Plan)
	case "codex":
		var tok, accountID string
		var err error
		if l.Current {
			home, herr := os.UserHomeDir()
			if herr != nil {
				err = herr
			} else {
				tok, accountID, err = codexToken(ctx, codexAuthPath(home))
			}
		} else {
			tok, accountID, err = savedCodexToken(ctx, l.User)
		}
		if err != nil {
			return SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: l.Plan, Windows: []QuotaWindow{}, Error: err.Error()}
		}
		q := codexQuotaWith(ctx, tok, accountID)
		q.Plan = l.Plan
		return q
	}
	return SubscriptionQuota{Windows: []QuotaWindow{}, Error: "no usage for " + l.Agent}
}
