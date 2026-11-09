package main

import (
	"fmt"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]             the subscription accounts magpie remembers
  magpie accounts switch <agent> <email>     sign the agent in to a remembered account
  magpie accounts forget <agent> <email>     forget a remembered account
  magpie accounts add <agent>                sign in to another account and remember it

agents: claude (cc), codex`

// accountsCmd runs `magpie accounts …`; args is what follows "accounts".
func accountsCmd(args []string) error {
	if len(args) == 0 {
		for _, agent := range []string{"claude", "codex"} {
			if err := listAccounts(agent); err != nil {
				return err
			}
		}
		return nil
	}
	switch args[0] {
	case "switch", "forget":
		if len(args) != 3 {
			return fmt.Errorf("%s takes an agent and an email\n\n%s", args[0], accountsUsage)
		}
		agent, ok := provider.AccountAgent(args[1])
		if !ok {
			return unknownAccountAgent(args[1])
		}
		if args[0] == "forget" {
			if err := provider.ForgetAccount(agent, args[2]); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", bold.Render(args[2]), muted.Render("("+agentTitleOf(agent)+")"))
			return nil
		}
		if err := provider.SwitchAccount(agent, args[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(agentTitleOf(agent)), "is signed in as", bold.Render(args[2]))
		fmt.Println(muted.Render("  sessions already running keep the account they started with; restart them to use this one"))
		return nil
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("add takes an agent\n\n%s", accountsUsage)
		}
		agent, ok := provider.AccountAgent(args[1])
		if !ok {
			return unknownAccountAgent(args[1])
		}
		acct, err := provider.AddAccount(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(agentTitleOf(agent)), "is signed in as", bold.Render(acct.User))
		return listAccounts(agent)
	}
	if len(args) != 1 {
		return fmt.Errorf("too many arguments\n\n%s", accountsUsage)
	}
	agent, ok := provider.AccountAgent(args[0])
	if !ok {
		return unknownAccountAgent(args[0])
	}
	return listAccounts(agent)
}

func unknownAccountAgent(name string) error {
	return fmt.Errorf("unknown agent %q\n\n%s", name, accountsUsage)
}

func agentTitleOf(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// listAccounts prints the accounts remembered for one agent, marking the one
// the agent is signed in to and each plan.
func listAccounts(agent string) error {
	accts, err := provider.RememberedAccounts(agent)
	if err != nil {
		return err
	}
	title := agentTitleOf(agent)
	if len(accts) == 0 {
		fmt.Println(bold.Render(title), muted.Render("— no accounts remembered; sign in to it, or run magpie accounts add "+agent))
		return nil
	}
	fmt.Println(bold.Render(title))
	for _, a := range accts {
		mark, state := faint.Render("○"), muted.Render("remembered")
		switch {
		case a.Active:
			mark, state = green.Render("●"), green.Render("signed in")
		case a.On:
			state = muted.Render("answers beside the signed-in account")
		}
		plan := ""
		if a.Plan != "" {
			plan = "  " + muted.Render(a.Plan)
		}
		fmt.Printf("  %s %s%s  %s\n", mark, a.User, plan, state)
	}
	return nil
}
