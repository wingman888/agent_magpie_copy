package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// QuotaWindow is one rolling allowance reported by a subscription provider.
type QuotaWindow struct {
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	ResetSecs int64      `json:"resetSecs,omitempty"`
	Display   string     `json:"display,omitempty"`
}

// SubscriptionQuota is provider-reported allowance usage. This is separate
// from Dial's local token log: vendors expose percentages, not token totals.
type SubscriptionQuota struct {
	Provider string        `json:"provider"`
	Name     string        `json:"name"`
	Icon     string        `json:"icon"`
	Plan     string        `json:"plan,omitempty"`
	Windows  []QuotaWindow `json:"windows"`
	Error    string        `json:"error,omitempty"`
}

var subscriptionUsageCache struct {
	sync.Mutex
	at   time.Time
	data []SubscriptionQuota
}

// SubscriptionUsage returns rolling quotas for signed-in first-party agents.
// Results are cached because these private account endpoints are aggressively
// rate limited when several CLI sessions are active.
func SubscriptionUsage(ctx context.Context) []SubscriptionQuota {
	subscriptionUsageCache.Lock()
	defer subscriptionUsageCache.Unlock()
	if time.Since(subscriptionUsageCache.at) < time.Minute && subscriptionUsageCache.data != nil {
		return append([]SubscriptionQuota(nil), subscriptionUsageCache.data...)
	}
	var out []SubscriptionQuota
	if _, _, ok := claudeCredential(); ok {
		out = append(out, claudeSubscriptionUsage(ctx))
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, ok := codexAccount(home); ok {
			out = append(out, codexSubscriptionUsage(ctx, filepath.Join(home, ".codex", "auth.json")))
		}
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		if app, ok := copilotLogin(cfg); ok {
			out = append(out, copilotSubscriptionUsage(ctx, app.Token))
		}
	}
	subscriptionUsageCache.at, subscriptionUsageCache.data = time.Now(), out
	return append([]SubscriptionQuota(nil), out...)
}

func accountJSON(ctx context.Context, url, token string, headers map[string]string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &accountStatusError{status: res.StatusCode}
	}
	return json.Unmarshal(b, dst)
}

type accountStatusError struct{ status int }

func (e *accountStatusError) Error() string { return http.StatusText(e.status) }

func claudeSubscriptionUsage(ctx context.Context) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Windows: []QuotaWindow{}}
	token, err := claudeToken(ctx)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	_, plan := claudeIdentity()
	q.Plan = plan
	var data struct {
		FiveHour       *quotaWire `json:"five_hour"`
		SevenDay       *quotaWire `json:"seven_day"`
		SevenDayOpus   *quotaWire `json:"seven_day_opus"`
		SevenDaySonnet *quotaWire `json:"seven_day_sonnet"`
	}
	err = accountJSON(ctx, "https://api.anthropic.com/api/oauth/usage", token, map[string]string{
		"anthropic-beta": "oauth-2025-04-20", "user-agent": "magpie",
	}, &data)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	for _, x := range []struct {
		name string
		w    *quotaWire
	}{{"5 hours", data.FiveHour}, {"7 days", data.SevenDay}, {"7 days · Opus", data.SevenDayOpus}, {"7 days · Sonnet", data.SevenDaySonnet}} {
		if x.w != nil {
			q.Windows = append(q.Windows, x.w.window(x.name))
		}
	}
	return q
}

type quotaWire struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

func (w quotaWire) window(name string) QuotaWindow {
	out := QuotaWindow{Name: name, Used: w.Utilization}
	if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
		out.ResetsAt = &t
	}
	return out
}

func codexSubscriptionUsage(ctx context.Context, path string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Windows: []QuotaWindow{}}
	token, accountID, err := codexToken(ctx, path)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	var data struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	base := strings.TrimSuffix(CodexBase, "/codex")
	err = accountJSON(ctx, base+"/wham/usage", token, map[string]string{"chatgpt-account-id": accountID}, &data)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan = data.PlanType
	if data.RateLimit.Primary != nil {
		q.Windows = append(q.Windows, data.RateLimit.Primary.window())
	}
	if data.RateLimit.Secondary != nil {
		q.Windows = append(q.Windows, data.RateLimit.Secondary.window())
	}
	return q
}

type codexWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	LimitWindowSecs int64   `json:"limit_window_seconds"`
	ResetAt         int64   `json:"reset_at"`
	ResetAfterSecs  int64   `json:"reset_after_seconds"`
}

func quotaDurationName(seconds int64) string {
	if seconds > 0 && seconds%(24*60*60) == 0 {
		return fmt.Sprintf("%d days", seconds/(24*60*60))
	}
	if seconds > 0 && seconds%(60*60) == 0 {
		return fmt.Sprintf("%d hours", seconds/(60*60))
	}
	return "Allowance"
}

func (w codexWindow) window() QuotaWindow {
	out := QuotaWindow{Name: quotaDurationName(w.LimitWindowSecs), Used: w.UsedPercent, ResetSecs: w.ResetAfterSecs}
	if w.ResetAt > 0 {
		t := time.Unix(w.ResetAt, 0)
		out.ResetsAt = &t
	}
	return out
}

func copilotSubscriptionUsage(ctx context.Context, githubToken string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "copilot", Name: "Copilot", Icon: "githubcopilot", Windows: []QuotaWindow{}}
	var data struct {
		Plan      string                      `json:"copilot_plan"`
		Snapshots map[string]copilotQuotaWire `json:"quota_snapshots"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/copilot_internal/user", nil)
	if err == nil {
		req.Header.Set("Authorization", "token "+githubToken)
		req.Header.Set("Accept", "application/json")
		for k, v := range copilotHeaders {
			req.Header.Set(k, v)
		}
		var res *http.Response
		res, err = http.DefaultClient.Do(req)
		if err == nil {
			defer res.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				err = &accountStatusError{status: res.StatusCode}
			} else {
				err = json.Unmarshal(b, &data)
			}
		}
	}
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan = data.Plan
	for _, x := range []struct{ id, name string }{{"chat", "Chat requests"}, {"completions", "Completions"}, {"premium_interactions", "Premium requests"}} {
		w, ok := data.Snapshots[x.id]
		if !ok || !w.HasQuota || w.Entitlement <= 0 {
			continue
		}
		used := w.Entitlement - w.Remaining
		q.Windows = append(q.Windows, QuotaWindow{Name: x.name, Used: 100 * used / w.Entitlement,
			Display: fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(w.Entitlement))})
	}
	return q
}

type copilotQuotaWire struct {
	HasQuota    bool    `json:"has_quota"`
	Entitlement float64 `json:"entitlement"`
	Remaining   float64 `json:"quota_remaining"`
}

func compactNumber(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d", int64(n))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", n), "0"), ".")
}
