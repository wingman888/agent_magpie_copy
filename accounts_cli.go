package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]              list the subscriptions magpie remembers
  magpie accounts switch <agent> <email>      sign the agent in as a remembered account
  magpie accounts add <agent>                 sign in to another account and remember it
  magpie accounts forget <agent> <email>      forget an account (not the one signed in)

  agent: claude (cc) or codex`

func accountsErr(msg string) error {
	if msg == "" {
		return errors.New(accountsUsage)
	}
	return errors.New(msg + "\n\n" + accountsUsage)
}

// agentLabel is the name shown for a remembered-login agent.
func agentLabel(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// accountsCmd: `magpie accounts [claude|codex]`, `switch`, `add`, `forget`.
func accountsCmd(args []string) error {
	if len(args) == 0 {
		for _, a := range []string{"claude", "codex"} {
			if err := listAccounts(a); err != nil {
				return err
			}
			fmt.Println()
		}
		return nil
	}
	verb := strings.ToLower(args[0])
	switch verb {
	case "switch", "add", "forget":
		return accountsVerb(verb, args[1:])
	}
	agent, err := provider.CanonicalLoginAgent(args[0])
	if err != nil {
		return accountsErr(err.Error())
	}
	if len(args) != 1 {
		return accountsErr("")
	}
	return listAccounts(agent)
}

func accountsVerb(verb string, rest []string) error {
	if verb == "add" {
		if len(rest) != 1 {
			return accountsErr("")
		}
		agent, err := provider.CanonicalLoginAgent(rest[0])
		if err != nil {
			return accountsErr(err.Error())
		}
		fmt.Println(muted.Render("opening " + agentLabel(agent) + "'s sign-in · if no browser opens, open the link it prints"))
		user, err := provider.AddLogin(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", user)
		fmt.Println()
		return listAccounts(agent)
	}
	if len(rest) != 2 {
		return accountsErr("")
	}
	agent, err := provider.CanonicalLoginAgent(rest[0])
	if err != nil {
		return accountsErr(err.Error())
	}
	user := strings.TrimSpace(rest[1])
	if verb == "switch" {
		name, err := provider.SwitchLogin(agent, user)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), agentLabel(agent), "is signed in as", name)
		fmt.Println(muted.Render("sessions already running keep the old account until they restart"))
		return nil
	}
	name, err := provider.ForgetLogin(agent, user)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "forgot", name, muted.Render("· "+agentLabel(agent)))
	return nil
}

// listAccounts prints the remembered accounts of one agent.
func listAccounts(agent string) error {
	logins, err := provider.Logins(agent)
	if err != nil {
		return err
	}
	fmt.Println(bold.Render(agentLabel(agent)))
	if len(logins) == 0 {
		fmt.Println(" ", muted.Render("no accounts remembered · magpie accounts add "+agent))
		return nil
	}
	// usage is best effort: a vendor that is slow or down leaves its column empty
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usage := provider.LoginUsage(ctx, agent)
	w := 0
	for _, l := range logins {
		w = max(w, len(l.User))
	}
	for _, l := range logins {
		mark, note := faint.Render("○"), ""
		switch {
		case l.Current:
			mark, note = green.Render("●"), green.Render("signed in")
		case l.On:
			note = muted.Render("used alongside")
		}
		if u, ok := usage[l.User]; ok {
			if s := usageText(u); s != "" {
				note += "  " + muted.Render(s)
			}
		}
		plan := ""
		if l.Plan != "" {
			plan = muted.Render(l.Plan)
		}
		fmt.Printf("  %s %s  %s  %s\n", mark, pad(l.User, w), pad(plan, 6), note)
	}
	return nil
}

// usageText is one account's windows as "5 hours 16% · 7 days 3%".
func usageText(u provider.SubscriptionQuota) string {
	if u.Error != "" {
		return "usage unavailable"
	}
	var parts []string
	for _, w := range u.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return strings.Join(parts, " · ")
}
