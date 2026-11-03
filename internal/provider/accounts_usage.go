package provider

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// loginUsageTTL is how long LoginUsage reuses its last answer.
const loginUsageTTL = time.Minute

var loginUsage = struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent → email → quota
}{at: map[string]time.Time{}, data: map[string]map[string]SubscriptionQuota{}}

// LoginUsage returns the rolling usage of every remembered account of agent
// (claude or codex), keyed by email. Each account is queried with its own
// credentials, so accounts that are not the current login count too. An
// answer is reused for a minute. When one account's query fails it keeps its
// last known answer. An unsupported agent gets an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	agent, err := accountAgent(agent)
	if err != nil {
		return map[string]SubscriptionQuota{}
	}
	loginUsage.Lock()
	if time.Since(loginUsage.at[agent]) < loginUsageTTL && loginUsage.data[agent] != nil {
		out := copyQuotas(loginUsage.data[agent])
		loginUsage.Unlock()
		return out
	}
	prev := copyQuotas(loginUsage.data[agent])
	loginUsage.Unlock()

	cur := currentLogin(agent)
	res := map[string]SubscriptionQuota{}
	for _, r := range loadAccounts().Accounts {
		if r.Agent != agent {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
		var q SubscriptionQuota
		if cur != nil && sameUser(cur.User, r.User) {
			q = currentQuota(qctx, agent)
		} else {
			q = storedQuota(qctx, r)
		}
		cancel()
		if old, ok := prev[r.User]; ok && q.Error != "" && old.Error == "" {
			res[r.User] = old
			continue
		}
		res[r.User] = q
	}

	loginUsage.Lock()
	loginUsage.at[agent] = time.Now()
	loginUsage.data[agent] = res
	loginUsage.Unlock()
	return copyQuotas(res)
}

// currentQuota queries the account the agent is signed in as.
func currentQuota(ctx context.Context, agent string) SubscriptionQuota {
	if agent == "codex" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Error: err.Error()}
		}
		return codexSubscriptionUsage(ctx, filepath.Join(home, ".codex", "auth.json"))
	}
	return claudeSubscriptionUsage(ctx)
}

// storedQuota queries a remembered account with its own copy of its credentials.
func storedQuota(ctx context.Context, r rememberedAccount) SubscriptionQuota {
	if r.Agent == "codex" {
		q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: r.Plan, Windows: []QuotaWindow{}}
		token, accountID, err := storedCodexToken(ctx, r.User)
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return fillCodexUsage(ctx, q, token, accountID)
	}
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: r.Plan, Windows: []QuotaWindow{}}
	token, err := storedClaudeToken(ctx, r.User)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	return fillClaudeUsage(ctx, q, token)
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
