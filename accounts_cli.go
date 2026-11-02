package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]               list the remembered subscription accounts (agent: claude, alias cc, or codex)
  magpie accounts switch <agent> <email>       sign the agent in to a remembered account
  magpie accounts forget <agent> <email>       forget a remembered account (not the one signed in)
  magpie accounts add <agent>                  sign in to one more account and remember it
  magpie accounts live <agent> <email> [on|off]  let an account work alongside the signed-in one (it takes over when that one runs out)`

func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		return listAccounts("claude", "codex")
	}
	verb := strings.ToLower(rest[0])
	switch verb {
	case "switch", "forget", "add", "live":
		if len(rest) < 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, ok := provider.LoginAgent(rest[1])
		if !ok {
			return fmt.Errorf("unknown agent %q\n\n%s", rest[1], accountsUsage)
		}
		return accountsVerb(verb, agent, rest[2:])
	}
	agent, ok := provider.LoginAgent(rest[0])
	if !ok || len(rest) > 1 {
		return fmt.Errorf("unknown agent or command %q\n\n%s", rest[0], accountsUsage)
	}
	return listAccounts(agent)
}

func accountsVerb(verb, agent string, rest []string) error {
	switch verb {
	case "switch":
		if len(rest) != 1 {
			return fmt.Errorf("%s", accountsUsage)
		}
		l, err := provider.SwitchLogin(agent, rest[0])
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), agent, "is now signed in as", bold.Render(l.User))
		fmt.Println(muted.Render("  sessions that are already running keep the previous account until you restart them"))
		return nil
	case "forget":
		if len(rest) != 1 {
			return fmt.Errorf("%s", accountsUsage)
		}
		if err := provider.ForgetLogin(agent, rest[0]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", bold.Render(rest[0]))
		return nil
	case "live":
		if len(rest) < 1 || len(rest) > 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		on := true
		if len(rest) == 2 {
			switch strings.ToLower(rest[1]) {
			case "on":
			case "off":
				on = false
			default:
				return fmt.Errorf("%s", accountsUsage)
			}
		}
		if err := provider.SetLoginOn(agent, rest[0], on); err != nil {
			return err
		}
		state := "works alongside the signed-in account"
		if !on {
			state = "no longer works alongside the signed-in account"
		}
		fmt.Println(green.Render("✓"), bold.Render(canonicalUser(agent, rest[0])), state)
		return nil
	case "add":
		if len(rest) != 0 {
			return fmt.Errorf("%s", accountsUsage)
		}
		fmt.Println(muted.Render("opening the " + agent + " sign-in page — sign in to the account to add, then come back"))
		l, err := provider.AddLogin(context.Background(), agent)
		if err != nil {
			return err
		}
		if !l.Current {
			fmt.Println(muted.Render("  " + agent + " stays signed in to its previous account; " + l.User + " works alongside it. magpie accounts switch " + agent + " " + l.User + " to sign the agent in to it."))
		}
		fmt.Println(green.Render("✓"), "account remembered")
		return listAccounts(agent)
	}
	return fmt.Errorf("%s", accountsUsage)
}

// canonicalUser spells an account as it was remembered.
func canonicalUser(agent, user string) string {
	if list, err := provider.Logins(agent); err == nil {
		for _, l := range list {
			if strings.EqualFold(l.User, user) {
				return l.User
			}
		}
	}
	return user
}

func listAccounts(agents ...string) error {
	shown := false
	for _, agent := range agents {
		list, err := provider.Logins(agent)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			if len(agents) == 1 {
				fmt.Println(muted.Render("no " + agent + " accounts yet · sign in with the agent, or magpie accounts add " + agent))
			}
			continue
		}
		shown = true
		fmt.Println(bold.Render(agent))
		for _, l := range list {
			mark, tag := "  ", ""
			switch {
			case l.Current:
				mark, tag = green.Render("● "), "signed in"
			case l.On:
				tag = "also on"
			}
			plan := ""
			if l.Plan != "" {
				plan = "  " + l.Plan
			}
			fmt.Println(" ", mark+l.User+muted.Render(plan), muted.Render(tag))
		}
	}
	if !shown && len(agents) > 1 {
		fmt.Println(muted.Render("no subscription accounts yet · sign in to claude or codex, or magpie accounts add <agent>"))
	}
	return nil
}
