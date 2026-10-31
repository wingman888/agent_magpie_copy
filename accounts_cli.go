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
  magpie accounts forget <agent> <email>    forget a remembered account (not the one signed in)
  magpie accounts add <agent>               sign in to another account through the vendor's login page

  agent: claude (alias cc) or codex
  e.g. magpie accounts switch codex me@example.com`

// accountsCmd: `magpie accounts …`
func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) > 0 {
		switch strings.ToLower(rest[0]) {
		case "switch", "use":
			return accountsSwitch(rest[1:])
		case "forget", "rm":
			return accountsForget(rest[1:])
		case "add", "login":
			return accountsAdd(rest[1:])
		}
	}
	if len(rest) > 1 {
		return fmt.Errorf("too many arguments\n\n%s", accountsUsage)
	}
	agents := provider.LoginAgents
	if len(rest) == 1 {
		a, err := agentArg(rest[0])
		if err != nil {
			return err
		}
		agents = []string{a}
	}
	return listAccounts(agents)
}

func agentArg(name string) (string, error) {
	a, err := provider.LoginAgent(name)
	if err != nil {
		return "", fmt.Errorf("%v\n\n%s", err, accountsUsage)
	}
	return a, nil
}

func listAccounts(agents []string) error {
	shown := 0
	for _, a := range agents {
		logins, err := provider.Logins(a)
		if err != nil {
			return err
		}
		if len(logins) == 0 {
			if len(agents) == 1 {
				fmt.Println(muted.Render("no " + a + " accounts yet · sign in with the agent, or magpie accounts add " + a))
			}
			continue
		}
		if shown > 0 {
			fmt.Println()
		}
		shown++
		fmt.Println(bold.Render(a))
		for _, l := range logins {
			mark, note := " ", ""
			switch {
			case l.Current:
				mark, note = green.Render("●"), green.Render("signed in")
			case l.On:
				mark, note = green.Render("◐"), muted.Render("active alongside")
			case !l.Saved:
				note = amber.Render("no credentials · sign in again")
			}
			plan := ""
			if l.Plan != "" {
				plan = muted.Render(l.Plan)
			}
			fmt.Printf("  %s %s  %s  %s\n", mark, l.User, plan, note)
		}
	}
	if shown == 0 && len(agents) > 1 {
		fmt.Println(muted.Render("no accounts yet · sign in to Claude Code or Codex, or magpie accounts add <agent>"))
	}
	return nil
}

func accountsSwitch(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("magpie accounts switch <agent> <email>\n\n%s", accountsUsage)
	}
	a, err := agentArg(args[0])
	if err != nil {
		return err
	}
	l, err := provider.SwitchLogin(a, args[1])
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), bold.Render(a), "now signed in as", l.User)
	fmt.Println(muted.Render("  ↻ sessions already running keep the previous account until you restart them"))
	return nil
}

func accountsForget(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("magpie accounts forget <agent> <email>\n\n%s", accountsUsage)
	}
	a, err := agentArg(args[0])
	if err != nil {
		return err
	}
	if err := provider.ForgetLogin(a, args[1]); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "forgot", args[1], muted.Render("("+a+")"))
	return nil
}

func accountsAdd(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("magpie accounts add <agent>\n\n%s", accountsUsage)
	}
	a, err := agentArg(args[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println(muted.Render("opening the " + a + " login page — sign in with the account to add…"))
	l, err := provider.AddLogin(ctx, a, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "remembered", l.User)
	fmt.Println()
	return listAccounts([]string{a})
}
