package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            list the subscription accounts magpie remembers
  magpie accounts switch <agent> <email>    sign the agent in as a remembered account
  magpie accounts add <agent>               sign in to another account and remember it
  magpie accounts forget <agent> <email>    forget a remembered account (not the one signed in)
  magpie accounts on <agent> <email>        keep a remembered account serving the gateway too
  magpie accounts off <agent> <email>       stop that

  agent: claude (cc) or codex`

// accountsCmd: `magpie accounts …`
func accountsCmd(args []string) error {
	if len(args) == 0 {
		return listAccounts([]string{"claude", "codex"})
	}
	switch args[0] {
	case "switch", "forget", "add", "on", "off":
		return accountAction(args[0], args[1:])
	}
	name, ok := provider.LoginAgent(args[0])
	if !ok {
		return fmt.Errorf("unknown agent %q\n\n%s", args[0], accountsUsage)
	}
	if len(args) > 1 {
		return fmt.Errorf("too many arguments\n\n%s", accountsUsage)
	}
	return listAccounts([]string{name})
}

// accountAction runs the subcommands that change something.
func accountAction(verb string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s needs an agent\n\n%s", verb, accountsUsage)
	}
	name, ok := provider.LoginAgent(args[0])
	if !ok {
		return fmt.Errorf("unknown agent %q\n\n%s", args[0], accountsUsage)
	}
	if verb == "add" {
		if len(args) != 1 {
			return fmt.Errorf("add takes only an agent\n\n%s", accountsUsage)
		}
		user, err := provider.AddLogin(name)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(agentName(name)), "is signed in as", user, muted.Render("· remembered"))
		return listAccounts([]string{name})
	}
	if len(args) != 2 {
		return fmt.Errorf("%s needs an agent and an email\n\n%s", verb, accountsUsage)
	}
	user := strings.TrimSpace(args[1])
	switch verb {
	case "switch":
		switched, err := provider.SwitchLogin(name, user)
		if err != nil {
			return err
		}
		if !switched {
			fmt.Println(muted.Render(agentName(name) + " is already signed in as " + user))
			return nil
		}
		fmt.Println(green.Render("✓"), bold.Render(agentName(name)), "is signed in as", user)
		fmt.Println(muted.Render("  ↻ sessions already running keep the account they started with; restart them to use " + user))
	case "forget":
		if err := provider.ForgetLogin(name, user); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", user, muted.Render("for "+agentName(name)))
	case "on", "off":
		if err := provider.SetLoginOn(name, user, verb == "on"); err != nil {
			return err
		}
		state := "also on: it serves the gateway next to the one signed in"
		if verb == "off" {
			state = "off: it no longer serves the gateway"
		}
		fmt.Println(green.Render("✓"), user, muted.Render(state))
	}
	return nil
}

// listAccounts prints the remembered logins of each agent, the one signed in
// first, with its plan and the 5-hour and weekly usage when it can be read.
func listAccounts(agents []string) error {
	for _, name := range agents {
		logins := provider.Logins(name)
		fmt.Println(bold.Render(agentName(name)))
		if len(logins) == 0 {
			fmt.Println("  " + muted.Render("no remembered accounts · magpie accounts add "+name+" signs one in"))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		usage := provider.LoginUsage(ctx, name)
		cancel()
		for _, l := range logins {
			mark, state := faint.Render("○"), muted.Render("off")
			switch {
			case l.Current:
				mark, state = green.Render("●"), green.Render("signed in")
			case l.On:
				state = muted.Render("also on")
			}
			plan := ""
			if l.Plan != "" {
				plan = muted.Render(l.Plan)
			}
			fmt.Printf("  %s %s  %s  %s%s\n", mark, bold.Render(l.User), plan, state, usageNote(usage[strings.ToLower(l.User)]))
		}
	}
	return nil
}

// usageNote is the usage of one login, as a short suffix; empty when unknown.
func usageNote(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return "  " + muted.Render("usage: "+q.Error)
	}
	if len(q.Windows) == 0 {
		return ""
	}
	var parts []string
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return "  " + muted.Render("used: "+strings.Join(parts, " · "))
}

// agentName is the agent's display name, or its id when magpie does not know it.
func agentName(name string) string {
	if a, err := agent.Find(name); err == nil {
		return a.Name
	}
	return name
}
