package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]           the remembered accounts, which one is in use, and their usage
  magpie accounts switch <agent> <email>   sign the agent in as a remembered account
  magpie accounts forget <agent> <email>   forget a remembered account
  magpie accounts add <agent>              open the sign-in page, then remember the account
  magpie accounts on|off <agent> <email>   let a remembered account serve beside the current one

agents: claude (cc), codex
`

// agentTitles name the agents in the output.
var agentTitles = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// signIn is the command that opens each agent's sign-in page.
var signIn = map[string][]string{"claude": {"claude", "auth", "login"}, "codex": {"codex", "login"}}

func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		return listAccounts("")
	}
	switch rest[0] {
	case "switch", "forget", "on", "off", "add":
		return accountAction(rest[0], rest[1:])
	}
	if len(rest) == 1 {
		if agent, ok := provider.CanonicalAgent(rest[0]); ok {
			return listAccounts(agent)
		}
	}
	return fmt.Errorf("unknown agent or command %q\n%s", strings.Join(rest, " "), accountsUsage)
}

func accountAction(cmd string, args []string) error {
	need := 2
	if cmd == "add" {
		need = 1
	}
	if len(args) != need {
		return errors.New(accountsUsage)
	}
	agent, ok := provider.CanonicalAgent(args[0])
	if !ok {
		return fmt.Errorf("unknown agent %q\n%s", args[0], accountsUsage)
	}
	switch cmd {
	case "add":
		return addAccount(agent)
	case "switch":
		if err := provider.SwitchLogin(agent, args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), agentTitles[agent], "is now signed in as", args[1]+".")
		fmt.Println(muted.Render("  sessions already running keep their old account until they restart"))
		return nil
	case "forget":
		if err := provider.ForgetLogin(agent, args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", args[1], "("+agentTitles[agent]+")")
		return nil
	case "on", "off":
		if err := provider.SetLoginOn(agent, args[1], cmd == "on"); err != nil {
			return err
		}
		state := "serves beside the current account"
		if cmd == "off" {
			state = "no longer serves"
		}
		fmt.Println(green.Render("✓"), args[1], state)
		return nil
	}
	return errors.New(accountsUsage)
}

// addAccount runs the agent's own sign-in, then remembers the account it
// leaves signed in and lists the accounts.
func addAccount(agent string) error {
	argv := signIn[agent]
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("%s is not installed; install it, then run magpie accounts add %s", agentTitles[agent], agent)
	}
	login := func() error {
		c := exec.Command(bin, argv[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}
	list, err := provider.AddLogin(agent, login)
	printLogins(agent, list, false)
	if err != nil {
		return fmt.Errorf("sign-in did not finish: %v", err)
	}
	return nil
}

func listAccounts(agent string) error {
	agents := []string{"claude", "codex"}
	if agent != "" {
		agents = []string{agent}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, a := range agents {
		ls := provider.Logins(a)
		printLogins(a, ls, true)
		if len(ls) > 0 {
			printUsage(ctx, a, ls)
		}
	}
	return nil
}

func printLogins(agent string, ls []provider.Login, header bool) {
	if header {
		fmt.Println(bold.Render(agentTitles[agent]))
	}
	if len(ls) == 0 {
		fmt.Println(muted.Render("  no remembered accounts; sign in to " + agentTitles[agent] + ", or run magpie accounts add " + agent))
		return
	}
	for _, l := range ls {
		mark, state := faint.Render("○"), "remembered"
		if l.Current {
			mark, state = green.Render("●"), "signed in"
		} else if l.On {
			state = "serving"
		}
		plan := l.Plan
		if plan == "" {
			plan = "-"
		}
		fmt.Printf("  %s %-34s %-10s %s\n", mark, l.User, plan, muted.Render(state))
	}
}

func printUsage(ctx context.Context, agent string, ls []provider.Login) {
	usage := provider.LoginUsage(ctx, agent)
	for _, l := range ls {
		q, ok := usage[l.User]
		if !ok {
			continue
		}
		if len(q.Windows) == 0 {
			fmt.Println("    " + muted.Render("usage unavailable: "+q.Error))
			continue
		}
		var parts []string
		for _, w := range q.Windows {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
		}
		fmt.Println("    " + muted.Render(strings.Join(parts, " · ")))
	}
}
