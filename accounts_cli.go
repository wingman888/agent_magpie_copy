package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]               the sign-ins magpie remembers; the one in use is marked
  magpie accounts switch <agent> <email>       sign the agent in as a remembered account
  magpie accounts on <agent> <email>           let a remembered account work alongside the one in use
  magpie accounts off <agent> <email>          stop that; the one in use always works
  magpie accounts add <agent>                  sign in through the agent's own page, then remember the account
  magpie accounts forget <agent> <email>       forget a remembered account (the one in use cannot be forgotten)

  <agent> is claude (cc) or codex.`

// accountTitles names each agent as the listing shows it.
var accountTitles = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// accountsCmd: `magpie accounts [verb …]`
func accountsCmd(args []string) error {
	if len(args) == 0 {
		for _, agent := range []string{"claude", "codex"} {
			if err := listAccounts(agent); err != nil {
				return err
			}
		}
		return nil
	}
	verb := strings.ToLower(args[0])
	switch verb {
	case "switch", "on", "off", "forget":
		if len(args) != 3 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := provider.AccountAgent(args[1])
		if err != nil {
			return fmt.Errorf("%v\n\n%s", err, accountsUsage)
		}
		email := strings.TrimSpace(args[2])
		return accountVerb(verb, agent, email)
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := provider.AccountAgent(args[1])
		if err != nil {
			return fmt.Errorf("%v\n\n%s", err, accountsUsage)
		}
		user, err := provider.AddAccount(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "remembered", user)
		return listAccounts(agent)
	}
	if len(args) != 1 {
		return fmt.Errorf("%s", accountsUsage)
	}
	agent, err := provider.AccountAgent(args[0])
	if err != nil {
		return fmt.Errorf("%v\n\n%s", err, accountsUsage)
	}
	return listAccounts(agent)
}

func accountVerb(verb, agent, email string) error {
	title := accountTitles[agent]
	switch verb {
	case "switch":
		switched, err := provider.SwitchAccount(agent, email)
		if err != nil {
			return err
		}
		if !switched {
			fmt.Println(muted.Render(title + " is already signed in as " + email))
			return nil
		}
		fmt.Println(green.Render("✓"), "signed", title, "in as", email)
		fmt.Println(muted.Render("  " + title + " sessions already running keep the old account until they restart"))
	case "on", "off":
		if err := provider.SetLoginOn(agent, email, verb == "on"); err != nil {
			return err
		}
		if verb == "on" {
			fmt.Println(green.Render("✓"), email, "works alongside the "+title+" sign-in in use")
		} else {
			fmt.Println(green.Render("✓"), email, "no longer works alongside the "+title+" sign-in in use")
		}
	case "forget":
		if err := provider.ForgetAccount(agent, email); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "forgot", email)
	}
	return nil
}

// listAccounts prints one agent's remembered sign-ins.
func listAccounts(agent string) error {
	accts, err := provider.ListAccounts(agent)
	if err != nil {
		return err
	}
	fmt.Println(bold.Render(accountTitles[agent]))
	if len(accts) == 0 {
		fmt.Println(muted.Render("  no sign-ins remembered yet; sign in with " + accountTitles[agent] + " and magpie remembers it, or run magpie accounts add " + agent))
		return nil
	}
	// the vendors answer slowly; a listing should not wait on them for long
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usage := provider.LoginUsage(ctx, agent)
	for _, a := range accts {
		mark := faint.Render("○")
		state := ""
		switch {
		case a.Current:
			mark = green.Render("●")
			state = "in use"
		case a.On:
			state = "works alongside"
		}
		plan := ""
		if a.Plan != "" {
			plan = "  " + a.Plan
		}
		q, ok := usage[a.User]
		fmt.Printf("  %s %s%s  %s%s\n", mark, a.User, muted.Render(plan), muted.Render(state), muted.Render(quotaText(q, ok)))
	}
	return nil
}

// quotaText sums up a sign-in's usage windows, e.g. "  5 hours 12% · 7 days 40%".
func quotaText(q provider.SubscriptionQuota, ok bool) string {
	if !ok {
		return ""
	}
	if len(q.Windows) == 0 {
		if q.Error != "" {
			return "  usage unavailable"
		}
		return ""
	}
	parts := make([]string, 0, len(q.Windows))
	for _, w := range q.Windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return "  " + strings.Join(parts, " · ")
}
