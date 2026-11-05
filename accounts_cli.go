package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            list the subscription accounts magpie remembers
  magpie accounts switch <agent> <email>    sign the agent in as a remembered account
  magpie accounts forget <agent> <email>    forget a remembered account (not the one signed in)
  magpie accounts add <agent>               open the sign-in page and remember the account

agents: claude (cc), codex`

// accountsCmd handles `magpie accounts …`; args are what follows "accounts".
func accountsCmd(args []string) error {
	if len(args) == 0 {
		for _, a := range []string{"claude", "codex"} {
			if err := listAccounts(a); err != nil {
				return err
			}
		}
		return nil
	}
	switch args[0] {
	case "switch", "forget":
		if len(args) != 3 {
			return accountsUsageErr("")
		}
		agent, ok := provider.LoginAgent(args[1])
		if !ok {
			return accountsUsageErr(fmt.Sprintf("unknown agent %q", args[1]))
		}
		if args[0] == "switch" {
			return switchAccount(agent, args[2])
		}
		return forgetAccount(agent, args[2])
	case "add":
		if len(args) != 2 {
			return accountsUsageErr("")
		}
		agent, ok := provider.LoginAgent(args[1])
		if !ok {
			return accountsUsageErr(fmt.Sprintf("unknown agent %q", args[1]))
		}
		return addAccount(agent)
	}
	agent, ok := provider.LoginAgent(args[0])
	if !ok {
		return accountsUsageErr(fmt.Sprintf("unknown agent %q", args[0]))
	}
	if len(args) > 1 {
		return accountsUsageErr("")
	}
	return listAccounts(agent)
}

// accountsUsageErr is the error for a bad `magpie accounts` line: what was
// wrong, then the usage text. The exit status is non-zero.
func accountsUsageErr(problem string) error {
	if problem == "" {
		return fmt.Errorf("%s", accountsUsage)
	}
	return fmt.Errorf("%s\n%s", problem, accountsUsage)
}

func agentLabel(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// listAccounts prints an agent's remembered accounts, marking the one signed
// in and any that serve alongside it, with their plan and usage.
func listAccounts(agent string) error {
	logins, err := provider.Logins(agent)
	if err != nil {
		return err
	}
	fmt.Println(bold.Render(agentLabel(agent)))
	if len(logins) == 0 {
		fmt.Println("  " + muted.Render("no accounts yet · magpie accounts add "+agent))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage := provider.LoginUsage(ctx, agent)
	for _, l := range logins {
		mark, state := faint.Render("○"), ""
		if l.Current {
			mark, state = green.Render("●"), green.Render("signed in")
		} else if l.On {
			state = muted.Render("on, serves alongside")
		}
		line := fmt.Sprintf("  %s %s", mark, bold.Render(l.User))
		if l.Plan != "" {
			line += "  " + muted.Render(l.Plan)
		}
		if state != "" {
			line += "  " + state
		}
		if q, ok := usage[l.User]; ok {
			line += "  " + muted.Render(usageText(q))
		}
		fmt.Println(line)
	}
	return nil
}

// usageText is a short account of how much of each window is used.
func usageText(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return "usage unavailable"
	}
	var parts []string
	if q.FiveHour != nil {
		parts = append(parts, fmt.Sprintf("5h %.0f%%", *q.FiveHour))
	}
	if q.Weekly != nil {
		parts = append(parts, fmt.Sprintf("week %.0f%%", *q.Weekly))
	}
	if len(parts) == 0 {
		return "no usage reported"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " · " + p
	}
	return out
}

func switchAccount(agent, email string) error {
	if err := provider.SwitchLogin(agent, email); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "signed "+agentLabel(agent)+" in as "+email)
	fmt.Println(muted.Render("sessions already running keep the account they started with; restart them to use " + email))
	return nil
}

func forgetAccount(agent, email string) error {
	if err := provider.ForgetLogin(agent, email); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "forgot "+email)
	return nil
}

// addAccount opens the vendor's sign-in page, then remembers the account that
// came back signed in and lists the accounts.
func addAccount(agent string) error {
	cmd, err := provider.StartLogin(agent)
	if err != nil {
		return err
	}
	fmt.Println(muted.Render("opening " + agentLabel(agent) + "'s sign-in page; finish signing in there"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s sign-in did not finish: %v", agentLabel(agent), err)
	}
	l, err := provider.FinishLogin(agent)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "remembered "+l.User)
	return listAccounts(agent)
}
