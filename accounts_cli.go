package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts                          list the Claude Code and Codex accounts magpie remembers
  magpie accounts claude|cc|codex          list one agent's accounts
  magpie accounts switch <agent> <email>   sign the agent in as a remembered account
  magpie accounts on|off <agent> <email>   let a remembered account serve the gateway alongside the current one
  magpie accounts forget <agent> <email>   forget a remembered account (not the one signed in now)
  magpie accounts add <agent>              open the vendor's sign-in page, then remember the account

agent: claude (alias cc) or codex`

// accountAgent maps what the user typed to the agent id magpie uses.
func accountAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q\n\n%s", name, accountsUsage)
}

func accountAgentName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		return listAccounts("claude", "codex")
	}
	switch rest[0] {
	case "switch", "forget", "on", "off":
		if len(rest) != 3 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		email := strings.TrimSpace(rest[2])
		switch rest[0] {
		case "switch":
			if err := provider.SwitchLogin(agent, email); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), accountAgentName(agent), "now signed in as", email)
			fmt.Println(muted.Render("  sessions already running keep the old account until they restart"))
		case "forget":
			if err := provider.ForgetLogin(agent, email); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", email, muted.Render("("+accountAgentName(agent)+")"))
		case "on", "off":
			if err := provider.SetLoginOn(agent, email, rest[0] == "on"); err != nil {
				return err
			}
			if rest[0] == "on" {
				fmt.Println(green.Render("✓"), email, "serves alongside the current", accountAgentName(agent), "login")
			} else {
				fmt.Println(green.Render("✓"), email, "no longer serves alongside the current", accountAgentName(agent), "login")
			}
		}
		return nil
	case "add":
		if len(rest) != 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := accountAgent(rest[1])
		if err != nil {
			return err
		}
		user, err := provider.AddLogin(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "signed in to", accountAgentName(agent), "as", user, muted.Render("and remembered it"))
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

// listAccounts prints the remembered accounts of each agent, marking the one
// signed in now and the ones that serve alongside it.
func listAccounts(agents ...string) error {
	for _, agent := range agents {
		fmt.Println(bold.Render(accountAgentName(agent)))
		logins := provider.Logins(agent)
		if len(logins) == 0 {
			fmt.Println(muted.Render("  no accounts remembered yet; sign in with the agent, or magpie accounts add " + agent))
			continue
		}
		for _, l := range logins {
			mark := faint.Render("○")
			tag := ""
			switch {
			case l.Current:
				mark = green.Render("●")
				tag = "signed in now"
			case l.On:
				tag = "serves alongside"
			}
			who := l.User
			if l.Plan != "" {
				who += muted.Render("  " + l.Plan)
			}
			if tag != "" {
				who += muted.Render("  " + tag)
			}
			fmt.Println("  " + mark + " " + who)
		}
	}
	return nil
}
