package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            list the subscription accounts magpie remembers, with usage
  magpie accounts switch <agent> <email>    sign the agent in as a remembered account
  magpie accounts add <agent>               sign in to another account and remember it, on alongside the current one
  magpie accounts on|off <agent> <email>    keep a remembered account on alongside the one signed in
  magpie accounts forget <agent> <email>    forget a remembered account (the one signed in cannot be forgotten)

agents: claude (cc), codex`

var accountAgentLabels = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// accountsCmd: `magpie accounts …`
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listAccounts("")
	}
	verb := strings.ToLower(args[0])
	switch verb {
	case "switch", "forget", "on", "off":
		if len(args) != 3 {
			return fmt.Errorf("magpie accounts %s <agent> <email>\n\n%s", verb, accountsUsage)
		}
		agent, err := accountAgentArg(args[1])
		if err != nil {
			return err
		}
		return accountChange(verb, agent, args[2])
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("magpie accounts add <agent>\n\n%s", accountsUsage)
		}
		agent, err := accountAgentArg(args[1])
		if err != nil {
			return err
		}
		return addAccount(agent)
	}
	if len(args) != 1 {
		return fmt.Errorf("%s", accountsUsage)
	}
	agent, err := accountAgentArg(args[0])
	if err != nil {
		return err
	}
	return listAccounts(agent)
}

// accountAgentArg reads an agent name for the accounts commands, aliases included.
func accountAgentArg(name string) (string, error) {
	if a, ok := provider.AccountAgent(name); ok {
		return a, nil
	}
	return "", fmt.Errorf("unknown agent %q\n\n%s", name, accountsUsage)
}

// accountChange runs switch, forget, on or off for one remembered account.
func accountChange(verb, agent, email string) error {
	label := accountAgentLabels[agent]
	switch verb {
	case "switch":
		acct, err := provider.SwitchAccount(agent, email)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), label, "is signed in as", bold.Render(acct.User))
		fmt.Println(muted.Render("  sessions already running keep the previous account until they restart · 已运行的会话在重启前仍用旧账号"))
	case "forget":
		if err := provider.ForgetAccount(agent, email); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", email, muted.Render("("+label+")"))
	case "on", "off":
		if err := provider.SetLoginOn(agent, email, verb == "on"); err != nil {
			return err
		}
		state := "on alongside the signed-in account"
		if verb == "off" {
			state = "off"
		}
		fmt.Println(green.Render("✓"), email, muted.Render("is "+state+" ("+label+")"))
	}
	return nil
}

// addAccount runs the vendor's sign-in, remembers the account, and lists the accounts.
func addAccount(agent string) error {
	acct, err := provider.AddAccount(agent)
	if err != nil {
		return err
	}
	if acct.Current {
		fmt.Println(green.Render("✓"), "signed in again as", bold.Render(acct.User))
	} else {
		fmt.Println(green.Render("✓"), "remembered", bold.Render(acct.User), muted.Render("· on alongside the signed-in account"))
	}
	return listAccounts(agent)
}

// listAccounts prints the remembered accounts of one agent, or of both.
func listAccounts(only string) error {
	agents := []string{"claude", "codex"}
	if only != "" {
		agents = []string{only}
	}
	for i, a := range agents {
		rows, err := provider.RememberedAccounts(a)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(bold.Render(accountAgentLabels[a]))
		if len(rows) == 0 {
			fmt.Println("  " + muted.Render("no accounts yet · sign in with the agent, or magpie accounts add "+a))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		usage := provider.LoginUsage(ctx, a)
		cancel()
		w := 0
		for _, r := range rows {
			w = max(w, len(r.User))
		}
		for _, r := range rows {
			mark, note := faint.Render("○"), ""
			switch {
			case r.Current:
				mark, note = green.Render("●"), green.Render("signed in")
			case r.On:
				mark, note = green.Render("◌"), green.Render("on alongside")
			}
			plan := r.Plan
			if plan == "" {
				plan = "-"
			}
			line := fmt.Sprintf("  %s %s  %s  %s", mark, pad(r.User, w), pad(muted.Render(plan), 8), note)
			if q, ok := usage[r.User]; ok {
				line += "  " + muted.Render(usageSummary(q))
			}
			fmt.Println(strings.TrimRight(line, " "))
		}
	}
	return nil
}

// usageSummary is the 5-hour and weekly share of an account, in words.
func usageSummary(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return "usage unavailable"
	}
	var parts []string
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return strings.Join(parts, " · ")
}
