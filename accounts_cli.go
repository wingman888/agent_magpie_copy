package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]              list the subscriptions magpie remembers, with their usage
  magpie accounts switch <agent> <email>      sign the agent in as a remembered account
  magpie accounts on|off <agent> <email>      let a remembered account serve requests at the same time
  magpie accounts forget <agent> <email>      forget a remembered account (not the one signed in)
  magpie accounts add <agent>                 sign in to another account, and remember it
  agents: claude (cc), codex`

// accountAgent maps the agent names accounts takes to the agent's id.
func accountAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q\n\n%s", name, accountsUsage)
}

// accountsCmd: `magpie accounts …`
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listAccounts("")
	}
	switch verb := strings.ToLower(args[0]); verb {
	case "switch", "on", "off", "forget":
		if len(args) != 3 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(args[1])
		if err != nil {
			return err
		}
		switch verb {
		case "switch":
			user, err := provider.SwitchAccount(agent, args[2])
			if err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "signed", agentName(agent), "in as", user)
			fmt.Println(muted.Render("  sessions already running keep the account they started with; restart them to use " + user))
		case "on", "off":
			if err := provider.SetLoginOn(agent, args[2], verb == "on"); err != nil {
				return err
			}
			if verb == "on" {
				fmt.Println(green.Render("✓"), args[2], muted.Render("now serves "+agentName(agent)+" requests alongside the signed-in account"))
			} else {
				fmt.Println(green.Render("✓"), args[2], muted.Render("no longer serves "+agentName(agent)+" requests alongside the signed-in account"))
			}
		case "forget":
			if err := provider.ForgetAccount(agent, args[2]); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", args[2])
		}
		return nil
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(args[1])
		if err != nil {
			return err
		}
		user, err := provider.AddAccount(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", user, muted.Render("· "+agentName(agent)))
		return listAccounts(agent)
	}
	agent, err := accountAgent(args[0])
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("too many arguments\n\n%s", accountsUsage)
	}
	return listAccounts(agent)
}

// listAccounts prints the remembered accounts of agent ("" for both), the
// signed-in one first, each with its plan and rolling usage.
func listAccounts(agent string) error {
	agents := []string{"claude", "codex"}
	if agent != "" {
		agents = []string{agent}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i, a := range agents {
		if i > 0 {
			fmt.Println()
		}
		rows := provider.Remembered(a)
		usage := provider.LoginUsage(ctx, a)
		fmt.Println(bold.Render(agentName(a)))
		if len(rows) == 0 {
			fmt.Println(muted.Render("  no accounts yet · magpie accounts add " + a))
			continue
		}
		// the signed-in account first, the rest in the order they were saved
		var ordered []provider.RememberedAccount
		for _, r := range rows {
			if r.Current {
				ordered = append(ordered, r)
			}
		}
		for _, r := range rows {
			if !r.Current {
				ordered = append(ordered, r)
			}
		}
		for _, r := range ordered {
			mark, note := faint.Render("○"), ""
			if r.Current {
				mark, note = green.Render("●"), green.Render("signed in")
			} else if r.On {
				note = muted.Render("serves alongside")
			}
			line := "  " + mark + " " + r.User
			if r.Plan != "" {
				line += "  " + muted.Render(r.Plan)
			}
			if note != "" {
				line += "  " + note
			}
			fmt.Println(line)
			if q, ok := usage[r.User]; ok {
				for _, w := range q.Windows {
					fmt.Println("      " + muted.Render(fmt.Sprintf("%s  %s used", w.Name, quotaPercent(w))))
				}
				if q.Error != "" {
					fmt.Println("      " + muted.Render("usage unavailable: "+q.Error))
				}
			}
		}
	}
	return nil
}

func quotaPercent(w provider.QuotaWindow) string {
	if w.Display != "" {
		return w.Display
	}
	return fmt.Sprintf("%.0f%%", w.Used)
}

// agentName is the agent's name as the user knows it.
func agentName(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	}
	return agent
}
