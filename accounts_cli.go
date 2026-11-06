package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]                 list remembered sign-ins and their usage
  magpie accounts switch <agent> <email>         sign the agent in to a remembered account
  magpie accounts forget <agent> <email>         forget a remembered account
  magpie accounts add <agent>                    sign in to another account and remember it
agents: claude (cc), codex`

// accountsCmd handles `magpie accounts …`.
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listAccounts("")
	}
	switch args[0] {
	case "switch", "forget", "add":
		return accountsAction(args[0], args[1:])
	}
	agent, ok := provider.AgentID(args[0])
	if !ok || len(args) > 1 {
		return accountsUsageError(args[0])
	}
	return listAccounts(agent)
}

// accountsUsageError reports an agent magpie does not handle, with the usage.
func accountsUsageError(agent string) error {
	return fmt.Errorf("unknown agent %q\n%s", agent, accountsUsage)
}

func accountsAction(verb string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("%s", accountsUsage)
	}
	agent, ok := provider.AgentID(rest[0])
	if !ok {
		return accountsUsageError(rest[0])
	}
	switch verb {
	case "switch":
		if len(rest) != 2 {
			return fmt.Errorf("magpie accounts switch <claude|codex> <email>")
		}
		if err := provider.SwitchLogin(agent, rest[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), agent, "now signs in as", bold.Render(rest[1]))
		fmt.Println(muted.Render("sessions already running keep the old account; restart them to use this one"))
		return nil
	case "forget":
		if len(rest) != 2 {
			return fmt.Errorf("magpie accounts forget <claude|codex> <email>")
		}
		if err := provider.ForgetLogin(agent, rest[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", bold.Render(rest[1]), muted.Render("("+agent+")"))
		return nil
	case "add":
		if len(rest) != 1 {
			return fmt.Errorf("magpie accounts add <claude|codex>")
		}
		user, err := provider.AddLogin(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", bold.Render(user), muted.Render("("+agent+")"))
		return listAccounts(agent)
	}
	return accountsUsageError(verb)
}

// listAccounts prints the remembered sign-ins of one agent, or of all.
func listAccounts(agent string) error {
	logins := provider.Logins(agent)
	if len(logins) == 0 {
		fmt.Println(muted.Render("no remembered accounts · magpie accounts add claude|codex"))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage := map[string]map[string]provider.SubscriptionQuota{}
	for _, l := range logins {
		if _, ok := usage[l.Agent]; !ok {
			usage[l.Agent] = provider.LoginUsage(ctx, l.Agent)
		}
	}
	type row struct{ agent, user, state, on, plan, quota string }
	var rows []row
	userW, planW := 0, 0
	for _, l := range logins {
		state := "signed out"
		switch {
		case l.Current:
			state = "signed in"
		case l.NeedsLogin:
			state = "needs sign-in"
		}
		on := "off"
		if l.On || l.Current {
			on = "on"
		}
		r := row{agent: l.Agent, user: l.User, state: state, on: on, plan: l.Plan, quota: quotaLine(usage[l.Agent][l.User])}
		userW = max(userW, len(r.user))
		planW = max(planW, len(r.plan))
		rows = append(rows, r)
	}
	for _, r := range rows {
		line := fmt.Sprintf("  %-6s  %s  %s  %s  %s", r.agent, pad(r.user, userW), pad(r.plan, planW), pad(r.state, 12), r.on)
		if r.quota != "" {
			line += "  " + muted.Render(r.quota)
		}
		fmt.Println(line)
	}
	return nil
}

// quotaLine summarizes one account's windows, e.g. "5 hours 42% · 7 days 10%".
func quotaLine(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return "usage unavailable"
	}
	var parts []string
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return strings.Join(parts, " · ")
}
