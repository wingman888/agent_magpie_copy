package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            list the remembered sign-ins; the signed-in one is marked
  magpie accounts switch <agent> <email>    sign the agent in to a remembered account
  magpie accounts forget <agent> <email>    forget a remembered account (not the signed-in one)
  magpie accounts add <agent>               sign in to an account and remember it
  magpie accounts on|off <agent> <email>    keep an account on at the same time as the signed-in one

  <agent> is claude (alias cc) for Claude Code, or codex for Codex (ChatGPT)`

// accountAgent maps an agent name typed on the command line to its id.
func accountAgent(name string) (string, error) {
	switch strings.ToLower(name) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q\n%s", name, accountsUsage)
}

func agentTitle(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// accountsCmd runs `magpie accounts …`.
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listAccounts([]string{"claude", "codex"})
	}
	switch args[0] {
	case "switch", "forget", "on", "off":
		if len(args) != 3 {
			return errors.New(accountsUsage)
		}
		agent, err := accountAgent(args[1])
		if err != nil {
			return err
		}
		email := args[2]
		switch args[0] {
		case "switch":
			if err := provider.SwitchAccount(agent, email); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "signed", agentTitle(agent), "in as", email)
			fmt.Println("sessions already running keep the account they started with; restart them to use", email)
		case "forget":
			if err := provider.ForgetAccount(agent, email); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", email, "for", agentTitle(agent))
		case "on", "off":
			if err := provider.SetLoginOn(agent, email, args[0] == "on"); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), email, "is", map[bool]string{true: "on", false: "off"}[args[0] == "on"],
				"at the same time as the signed-in", agentTitle(agent), "account")
		}
		return nil
	case "add":
		if len(args) != 2 {
			return errors.New(accountsUsage)
		}
		agent, err := accountAgent(args[1])
		if err != nil {
			return err
		}
		return addAccount(agent)
	}
	agent, err := accountAgent(args[0])
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return errors.New(accountsUsage)
	}
	return listAccounts([]string{agent})
}

// listAccounts prints the remembered sign-ins of each agent.
func listAccounts(agents []string) error {
	for _, agent := range agents {
		list, err := provider.SavedAccounts(agent)
		if err != nil {
			return err
		}
		fmt.Println(bold.Render(agentTitle(agent)))
		if len(list) == 0 {
			fmt.Println("  " + muted.Render("none remembered yet; sign in with "+agentTitle(agent)+" or run magpie accounts add "+agent))
		}
		for _, a := range list {
			mark, state := "  ", ""
			switch {
			case a.Current:
				mark, state = green.Render("●")+" ", "signed in"
			case a.On:
				state = "on at the same time"
			}
			plan := a.Plan
			if plan == "" {
				plan = "-"
			}
			fmt.Printf("  %s%-34s %-12s %s\n", mark, a.User, plan, muted.Render(state))
		}
	}
	return nil
}

// loginCommand is the agent's own sign-in command; it opens the vendor's page
// and waits for the sign-in to come back.
func loginCommand(agent string) (string, []string) {
	if agent == "claude" {
		return "claude", []string{"auth", "login"}
	}
	return "codex", []string{"login"}
}

// loginPage is the vendor's page, shown when the agent's own command is not installed.
func loginPage(agent string) string {
	if agent == "claude" {
		return "https://claude.ai"
	}
	return "https://chatgpt.com/codex"
}

// openBrowser asks the system to open a page; false when it could not.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start() == nil
}

// addAccount signs in to another account through the agent's own login, then
// remembers whoever is signed in afterwards. The account being replaced is
// remembered first, so its latest login is kept.
func addAccount(agent string) error {
	if err := provider.RememberSignIn(agent); err != nil {
		return err
	}
	bin, args := loginCommand(agent)
	if path, err := exec.LookPath(bin); err == nil {
		fmt.Println("signing in to", agentTitle(agent), "; finish in the browser, magpie picks the account up when it returns")
		cmd := exec.Command(path, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
		}
	} else {
		page := loginPage(agent)
		if openBrowser(page) {
			fmt.Println("opened", page, "to sign in")
		} else {
			fmt.Println("open this page to sign in:", page)
		}
		fmt.Print("press Enter once you have signed in… ")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	provider.ForgetSignIn()
	if err := provider.RememberSignIn(agent); err != nil {
		return err
	}
	return listAccounts([]string{agent})
}
