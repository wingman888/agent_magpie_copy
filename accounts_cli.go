package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            list the subscription accounts magpie remembers
  magpie accounts switch <agent> <email>    sign the agent in to a remembered account
  magpie accounts forget <agent> <email>    forget an account (not the one signed in)
  magpie accounts add <agent>               sign in to another account in the browser, and remember it

  <agent> is claude (alias cc) or codex. Accounts the agent has been signed in to are remembered
  automatically. Several can work at once: when one runs out of quota the next takes over.`

func accountsError(format string, a ...any) error {
	return fmt.Errorf("%s\n\n%s", fmt.Sprintf(format, a...), accountsUsage)
}

func accountAgent(name string) (string, error) {
	a, ok := provider.LoginAgent(name)
	if !ok {
		return "", accountsError("unknown agent %q", name)
	}
	return a, nil
}

// accountsCmd: `magpie accounts …` — the subscription accounts of Claude Code and Codex
func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		for i, a := range []string{"claude", "codex"} {
			if i > 0 {
				fmt.Println()
			}
			if err := printAccounts(a); err != nil {
				return err
			}
		}
		return nil
	}
	switch strings.ToLower(rest[0]) {
	case "switch", "use":
		if len(rest) != 3 {
			return accountsError("accounts switch needs an agent and an email")
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		l, err := provider.SwitchLogin(agent, rest[2])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(agentName(agent)), "is signed in to", bold.Render(l.User), muted.Render(l.Plan))
		fmt.Println(muted.Render("  ↻ Sessions already running keep using the previous account until they are restarted."))
		return nil
	case "forget", "rm", "remove":
		if len(rest) != 3 {
			return accountsError("accounts forget needs an agent and an email")
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		if err := provider.ForgetLogin(agent, rest[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", bold.Render(rest[2]))
		return nil
	case "add", "login":
		if len(rest) != 2 {
			return accountsError("accounts add needs an agent")
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Println(muted.Render("opening " + agentName(agent) + "'s sign-in page — finish signing in, then come back here…"))
		l, err := provider.AddLogin(ctx, agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", bold.Render(l.User), muted.Render(l.Plan))
		if !l.Current && l.On {
			fmt.Println(muted.Render("  it works beside the account " + agentName(agent) + " is signed in to; the next takes over when one runs out"))
		}
		fmt.Println()
		return printAccounts(agent)
	}
	agent, err := accountAgent(rest[0])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return accountsError("too many arguments")
	}
	return printAccounts(agent)
}

func agentName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func printAccounts(agent string) error {
	logins, err := provider.Logins(agent)
	if err != nil {
		return err
	}
	fmt.Println(bold.Render(agentName(agent)))
	if len(logins) == 0 {
		fmt.Println(muted.Render("  no account yet — sign in with the agent, or: magpie accounts add " + agent))
		return nil
	}
	for _, l := range logins {
		mark, note := "  ", ""
		switch {
		case l.Current:
			mark, note = green.Render("● "), "signed in"
		case l.Lost:
			note = "sign-in lost; add it again"
		case l.On:
			mark, note = amber.Render("◐ "), "also on"
		}
		plan := l.Plan
		if plan == "" {
			plan = "-"
		}
		fmt.Printf("  %s%-32s %-8s %s\n", mark, l.User, plan, muted.Render(note))
	}
	return nil
}
