package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]                  list the remembered accounts (claude also as cc)
  magpie accounts switch <claude|codex> <email>   sign the agent in to a remembered account
  magpie accounts forget <claude|codex> <email>   forget a remembered account
  magpie accounts add <claude|codex>              sign in to another account and remember it
  magpie accounts on|off <claude|codex> <email>   serve requests alongside the current account`

// loginAgent names the agent an accounts command is about, or false when it
// is not one that keeps accounts.
func loginAgent(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func agentTitle(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// accountsCmd handles `magpie accounts …`.
func accountsCmd(args []string) error {
	rest := args[1:]
	usageErr := func(msg string) error { return fmt.Errorf("%s\n%s", msg, accountsUsage) }
	if len(rest) == 0 {
		return showLogins([]string{"claude", "codex"})
	}
	verb := rest[0]
	switch verb {
	case "add":
		if len(rest) != 2 {
			return usageErr("magpie accounts add <claude|codex>")
		}
		agent, ok := loginAgent(rest[1])
		if !ok {
			return usageErr(fmt.Sprintf("unknown agent %q", rest[1]))
		}
		return addLogin(agent)
	case "switch", "forget", "on", "off":
		if len(rest) != 3 {
			return usageErr(fmt.Sprintf("magpie accounts %s <claude|codex> <email>", verb))
		}
		agent, ok := loginAgent(rest[1])
		if !ok {
			return usageErr(fmt.Sprintf("unknown agent %q", rest[1]))
		}
		return changeLogin(verb, agent, rest[2])
	}
	if len(rest) != 1 {
		return usageErr("magpie accounts [claude|codex]")
	}
	agent, ok := loginAgent(verb)
	if !ok {
		return usageErr(fmt.Sprintf("unknown agent %q", verb))
	}
	return showLogins([]string{agent})
}

// changeLogin runs switch, forget, on or off for one remembered account.
func changeLogin(verb, agent, email string) error {
	switch verb {
	case "switch":
		if err := provider.SwitchLogin(agent, email); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "signed "+agentTitle(agent)+" in to", email)
		fmt.Println(muted.Render("sessions already running keep the previous account until they restart"))
	case "forget":
		if err := provider.ForgetLogin(agent, email); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", email, muted.Render("("+agentTitle(agent)+")"))
	case "on", "off":
		if err := provider.SetLoginOn(agent, email, verb == "on"); err != nil {
			return err
		}
		state := "serves requests alongside the current account"
		if verb == "off" {
			state = "no longer serves requests"
		}
		fmt.Println(green.Render("✓"), email, muted.Render(state))
	}
	return nil
}

// addLogin opens the agent's own sign-in, then remembers the account it leaves signed in.
func addLogin(agent string) error {
	argv, err := provider.LoginCommand(agent)
	if err != nil {
		return err
	}
	fmt.Println(muted.Render("signing in to " + agentTitle(agent) + "; finish the login in your browser"))
	login, err := provider.AddLogin(agent, func() error {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	})
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "remembered", login.User, muted.Render("("+agentTitle(agent)+")"))
	return showLogins([]string{agent})
}

// showLogins lists the remembered accounts of each agent, the current one marked.
func showLogins(agents []string) error {
	for _, agent := range agents {
		logins, err := provider.Logins(agent)
		if err != nil {
			return err
		}
		fmt.Println(bold.Render(agentTitle(agent)))
		if len(logins) == 0 {
			fmt.Println("  " + muted.Render("no remembered accounts; sign in with magpie accounts add "+agent))
			continue
		}
		for _, l := range logins {
			mark := "  "
			if l.Current {
				mark = green.Render("●") + " "
			}
			plan := l.Plan
			if plan == "" {
				plan = "-"
			}
			status := "off"
			switch {
			case l.Current:
				status = "signed in"
			case !l.Held:
				status = "signed out; sign in with magpie accounts add " + agent
			case l.On:
				status = "on"
			}
			fmt.Printf("  %s%-32s %-12s %s\n", mark, l.User, muted.Render(plan), muted.Render(status))
		}
	}
	return nil
}
