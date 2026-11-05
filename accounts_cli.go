package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]           list the subscription accounts magpie remembers
  magpie accounts switch <agent> <email>   sign the agent in to a remembered account
  magpie accounts forget <agent> <email>   forget a remembered account
  magpie accounts add <agent>              sign in to another account and remember it
agents: claude (cc), codex`

// accountAgent maps the agent names accepted on the command line.
func accountAgent(name string) (string, error) {
	switch strings.ToLower(name) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q\n%s", name, accountsUsage)
}

func accountTitle(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// accountsCmd handles `magpie accounts ...`; args[0] is "accounts".
func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		return listAccounts("claude", "codex")
	}
	switch rest[0] {
	case "switch":
		if len(rest) != 3 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		if err := provider.SwitchAccount(agent, rest[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), accountTitle(agent), "is now signed in to", rest[2])
		fmt.Println(muted.Render("  sessions already running keep the old account until they restart"))
		return nil
	case "forget":
		if len(rest) != 3 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		if err := provider.ForgetAccount(agent, rest[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", rest[2], muted.Render("("+accountTitle(agent)+")"))
		return nil
	case "add":
		if len(rest) != 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		if err := provider.Login(agent); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "signed in and remembered an account for", accountTitle(agent))
		return listAccounts(agent)
	}
	if len(rest) != 1 {
		return fmt.Errorf("%s", accountsUsage)
	}
	agent, err := accountAgent(rest[0])
	if err != nil {
		return err
	}
	return listAccounts(agent)
}

// listAccounts prints the remembered accounts of each agent, the one in use
// first, with its plan and the usage the vendor reports.
func listAccounts(agents ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, agent := range agents {
		fmt.Println(bold.Render(accountTitle(agent)))
		list := provider.Remembered(agent)
		if len(list) == 0 {
			fmt.Println(muted.Render("  no remembered accounts; sign in with the agent, or magpie accounts add " + agent))
			continue
		}
		usage := provider.LoginUsage(ctx, agent)
		for _, a := range list {
			mark, state := "○", "remembered"
			switch {
			case a.Live:
				mark, state = "●", "signed in"
			case a.On:
				state = "on alongside"
			}
			plan := ""
			if a.Plan != "" {
				plan = " " + a.Plan
			}
			fmt.Printf("  %s %s%s  %s\n", mark, a.User, muted.Render(plan), muted.Render(state))
			if q, ok := usage[a.User]; ok {
				fmt.Println("     ", usageLine(q))
			}
		}
	}
	return nil
}

// usageLine summarizes an account's windows, or why they are not known.
func usageLine(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return muted.Render("usage unknown: " + q.Error)
	}
	var parts []string
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	if len(parts) == 0 {
		return muted.Render("no usage reported")
	}
	return strings.Join(parts, " · ")
}
