package provider

// Per-account usage of the remembered logins: how much of each account's
// 5-hour and weekly windows is already used.

import (
	"context"
	"sync"
	"time"
)

// loginUsageTTL is how long a LoginUsage result is reused. A var so tests can shorten it.
var loginUsageTTL = time.Minute

type loginUsageEntry struct {
	at   time.Time
	data map[string]SubscriptionQuota // by account email
}

var (
	loginUsageMu    sync.Mutex
	loginUsageCache = map[string]loginUsageEntry{} // by agent id
)

// LoginUsage returns the usage of each remembered account of an agent, keyed
// by the account's email. Only claude and codex have such usage; any other
// agent gets an empty map. A result is reused for a minute. An account whose
// query fails keeps the result magpie already had for it; with no earlier
// result, it is reported with its error.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	id, err := CanonicalLoginAgent(agent)
	if err != nil {
		return map[string]SubscriptionQuota{}
	}
	loginUsageMu.Lock()
	e, cached := loginUsageCache[id]
	loginUsageMu.Unlock()
	if cached && time.Since(e.at) < loginUsageTTL {
		return copyQuotas(e.data)
	}
	logins, err := Logins(id)
	if err != nil {
		return map[string]SubscriptionQuota{}
	}
	out := make(map[string]SubscriptionQuota, len(logins))
	for _, l := range logins {
		q := loginQuota(ctx, id, l)
		if q.Error != "" {
			if prev, ok := e.data[l.User]; ok {
				out[l.User] = prev
				continue
			}
		}
		out[l.User] = q
	}
	loginUsageMu.Lock()
	loginUsageCache[id] = loginUsageEntry{at: time.Now(), data: out}
	loginUsageMu.Unlock()
	return copyQuotas(out)
}

// loginQuota reads one remembered account's usage. The account the agent is
// signed in as uses the agent's own tokens; the others use the ones magpie keeps.
func loginQuota(ctx context.Context, agent string, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Provider: agent, Email: l.User, Plan: l.Plan, Windows: []QuotaWindow{}}
	switch agent {
	case "claude":
		q.Name, q.Icon = "Claude Code", "claude-color"
		var tok string
		var err error
		if l.Current {
			tok, err = claudeToken(ctx)
		} else {
			tok, err = storedClaudeToken(ctx, l.User)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		q = claudeUsageWith(ctx, tok, q)
	case "codex":
		q.Name, q.Icon = "Codex", "codex-color"
		var tok, accountID string
		var err error
		if l.Current {
			tok, accountID, err = codexToken(ctx, codexAuthPath())
		} else {
			tok, accountID, err = storedCodexToken(ctx, l.User)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		q = codexUsageWith(ctx, tok, accountID, q)
	}
	for _, w := range q.Windows {
		switch w.Name {
		case "5 hours":
			q.FiveHour = w.Used
		case "7 days":
			q.Weekly = w.Used
		}
	}
	return q
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
