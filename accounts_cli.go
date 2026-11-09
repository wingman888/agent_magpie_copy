package main

import (
	"fmt"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]           list saved sign-ins; the one in use is marked
  magpie accounts switch <agent> <email>   sign the agent in to a saved sign-in
  magpie accounts forget <agent> <email>   drop a saved sign-in (not the one in use)
  magpie accounts add <agent>              open the sign-in page, then save the new sign-in

agents: claude (cc), codex`

// accountsCmd handles `magpie accounts …`.
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listLogins("")
	}
	switch args[0] {
	case "switch", "forget", "add":
	default:
		agent, ok := provider.LoginAgent(args[0])
		if !ok {
			return fmt.Errorf("unknown agent %q\n%s", args[0], accountsUsage)
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: magpie accounts %s\n%s", args[0], accountsUsage)
		}
		return listLogins(agent)
	}
	if len(args) < 2 {
		return fmt.Errorf("magpie accounts %s needs an agent\n%s", args[0], accountsUsage)
	}
	agent, ok := provider.LoginAgent(args[1])
	if !ok {
		return fmt.Errorf("unknown agent %q\n%s", args[1], accountsUsage)
	}
	switch args[0] {
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("usage: magpie accounts add <agent>\n%s", accountsUsage)
		}
		user, err := provider.AddLogin(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), agent, "signed in as", user, muted.Render("(saved)"))
		fmt.Println()
		return listLogins(agent)
	case "forget":
		if len(args) != 3 {
			return fmt.Errorf("usage: magpie accounts forget <agent> <email>\n%s", accountsUsage)
		}
		if err := provider.ForgetLogin(agent, args[2]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", args[2], "for", agent)
		return nil
	}
	// switch
	if len(args) != 3 {
		return fmt.Errorf("usage: magpie accounts switch <agent> <email>\n%s", accountsUsage)
	}
	changed, err := provider.SwitchLogin(agent, args[2])
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println(muted.Render(agent + " is already signed in as " + args[2]))
		return nil
	}
	fmt.Println(green.Render("✓"), agent, "is now signed in as", args[2])
	fmt.Println(muted.Render("Sessions already running keep the old account until they restart."))
	return nil
}

// listLogins prints the saved sign-ins of one agent, or of both when agent is "".
func listLogins(agent string) error {
	logins := provider.Logins(agent)
	if len(logins) == 0 {
		fmt.Println("no saved sign-ins; sign in with the agent, or run: magpie accounts add <agent>")
		return nil
	}
	for _, l := range logins {
		mark, state := muted.Render("○"), "saved"
		switch {
		case l.Current:
			mark, state = green.Render("●"), "signed in"
		case l.On:
			state = "saved, on at the same time"
		}
		plan := ""
		if l.Plan != "" {
			plan = "  " + l.Plan
		}
		fmt.Printf("%s %-7s %s%s  %s\n", mark, l.Agent, l.User, plan, muted.Render(state))
	}
	return nil
}
