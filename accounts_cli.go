package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]                  list the accounts each agent has signed in to
  magpie accounts switch <claude|codex> <email>   sign the agent in to a remembered account
  magpie accounts forget <claude|codex> <email>   forget a remembered account
  magpie accounts add <claude|codex>              sign in to another account and remember it

agents: claude (cc), codex`

// accountsCmd handles `magpie accounts`: listing the subscription accounts
// magpie remembers, switching the agent between them, forgetting one, or
// adding another sign-in.
func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		for _, agent := range provider.LoginAgents {
			if err := listAccounts(agent); err != nil {
				return err
			}
		}
		return nil
	}
	switch strings.ToLower(rest[0]) {
	case "switch", "forget":
		if len(rest) != 3 {
			return fmt.Errorf("usage: magpie accounts %s <claude|codex> <email>\n\n%s", rest[0], accountsUsage)
		}
		agent, ok := provider.AccountAgent(rest[1])
		if !ok {
			return fmt.Errorf("unknown agent %q\n\n%s", rest[1], accountsUsage)
		}
		if rest[0] == "switch" {
			if err := provider.SwitchLogin(agent, rest[2]); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "signed", agentLabel(agent), "in as", rest[2])
			fmt.Println("sessions already running keep the account they started with until they restart")
			return nil
		}
		if err := provider.ForgetLogin(agent, rest[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", rest[2], "for", agentLabel(agent))
		return nil
	case "add":
		if len(rest) != 2 {
			return fmt.Errorf("usage: magpie accounts add <claude|codex>\n\n%s", accountsUsage)
		}
		agent, ok := provider.AccountAgent(rest[1])
		if !ok {
			return fmt.Errorf("unknown agent %q\n\n%s", rest[1], accountsUsage)
		}
		fmt.Println("signing in to", agentLabel(agent), "- its sign-in page opens at", provider.LoginPage(agent))
		name, err := provider.AddAccount(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", name, "- it serves requests alongside the current login")
		return listAccounts(agent)
	}
	agent, ok := provider.AccountAgent(rest[0])
	if !ok {
		return fmt.Errorf("unknown agent %q\n\n%s", rest[0], accountsUsage)
	}
	if len(rest) > 1 {
		return fmt.Errorf("too many arguments\n\n%s", accountsUsage)
	}
	return listAccounts(agent)
}

// agentLabel is the name an agent goes by in the accounts list.
func agentLabel(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// listAccounts prints the accounts an agent has signed in to, marking the
// current login and the plan each one has.
func listAccounts(agent string) error {
	signIns := provider.SignIns(agent)
	fmt.Println(bold.Render(agentLabel(agent)))
	if len(signIns) == 0 {
		fmt.Println("  no accounts yet; sign in with magpie accounts add", agent)
		return nil
	}
	for _, s := range signIns {
		mark, status := " ", muted.Render("remembered")
		switch {
		case s.Current:
			mark, status = green.Render("●"), "signed in"
		case s.On:
			status = "serving alongside"
		}
		plan := s.Plan
		if plan == "" {
			plan = "-"
		}
		fmt.Printf("  %s %-36s %-10s %s\n", mark, s.User, plan, status)
	}
	return nil
}
