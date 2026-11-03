package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const accountsUsage = `usage:
  magpie accounts [claude|codex]            signed-in subscription accounts; the current one is marked
  magpie accounts switch <agent> <email>    make a remembered account the agent's login
  magpie accounts add <agent>               sign in with another account and remember it
  magpie accounts on|off <agent> <email>    serve an account alongside the current login
  magpie accounts forget <agent> <email>    forget an account (not the current login)

agents: claude (cc), codex`

// accountsCmd: `magpie accounts …` — the subscription accounts of Claude Code and Codex.
func accountsCmd(args []string) error {
	rest := args[1:]
	if len(rest) == 0 {
		return listAccounts("")
	}
	switch rest[0] {
	case "switch":
		agent, email, err := agentAndEmail(rest)
		if err != nil {
			return err
		}
		if err := provider.SwitchAccount(agent, email); err != nil {
			return err
		}
		fmt.Printf("%s now signs in as %s.\n", agentTitle(agent), email)
		fmt.Println(muted.Render("  sessions already running keep the old account until they restart"))
		return nil
	case "add":
		if len(rest) != 2 {
			return fmt.Errorf("%s", accountsUsage)
		}
		agent, err := knownAgent(rest[1])
		if err != nil {
			return err
		}
		user, err := provider.AddAccount(agent)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("signed in as " + user + " and remembered it"))
		return listAccounts(agent)
	case "on", "off":
		agent, email, err := agentAndEmail(rest)
		if err != nil {
			return err
		}
		if err := provider.SetLoginOn(agent, email, rest[0] == "on"); err != nil {
			return err
		}
		if rest[0] == "on" {
			fmt.Printf("%s is served alongside the current %s login.\n", email, agentTitle(agent))
		} else {
			fmt.Printf("%s is no longer served alongside the current %s login.\n", email, agentTitle(agent))
		}
		return nil
	case "forget":
		agent, email, err := agentAndEmail(rest)
		if err != nil {
			return err
		}
		if err := provider.ForgetAccount(agent, email); err != nil {
			return err
		}
		fmt.Println("forgot " + email + " (" + agentTitle(agent) + ")")
		return nil
	}
	if len(rest) > 1 {
		return fmt.Errorf("%s", accountsUsage)
	}
	agent, err := knownAgent(rest[0])
	if err != nil {
		return err
	}
	return listAccounts(agent)
}

// knownAgent resolves an agent name accounts can hold; anything else is an error with the usage.
func knownAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q\n\n%s", name, accountsUsage)
}

// agentAndEmail reads `<cmd> <agent> <email>` from args.
func agentAndEmail(args []string) (string, string, error) {
	if len(args) != 3 || strings.TrimSpace(args[2]) == "" {
		return "", "", fmt.Errorf("%s", accountsUsage)
	}
	agent, err := knownAgent(args[1])
	if err != nil {
		return "", "", err
	}
	return agent, strings.TrimSpace(args[2]), nil
}

func agentTitle(agent string) string {
	if agent == "codex" {
		return "Codex"
	}
	return "Claude Code"
}

// listAccounts prints the remembered accounts of agent, or of both when agent is empty.
func listAccounts(agent string) error {
	agents := []string{"claude", "codex"}
	if agent != "" {
		agents = []string{agent}
	}
	for _, a := range agents {
		infos, err := provider.RememberedAccounts(a)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			fmt.Println(bold.Render(agentTitle(a)) + muted.Render("  no signed-in account; run magpie accounts add "+a))
			continue
		}
		fmt.Println(bold.Render(agentTitle(a)))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		quotas := provider.LoginUsage(ctx, a)
		cancel()
		for _, info := range infos {
			state := muted.Render("standby")
			switch {
			case info.Current:
				state = green.Render("current")
			case info.On:
				state = green.Render("on")
			}
			mark := "  "
			if info.Current {
				mark = "● "
			}
			plan := info.Plan
			if plan == "" {
				plan = "-"
			}
			fmt.Printf("  %s%-32s %-10s %s%s\n", mark, info.User, plan, state, quotaSummary(quotas[info.User]))
		}
	}
	return nil
}

// quotaSummary renders the used share of each window, e.g. "  5 hours 12% · 7 days 40%".
func quotaSummary(q provider.SubscriptionQuota) string {
	if q.Error != "" {
		return "  " + muted.Render("usage unavailable: "+q.Error)
	}
	if len(q.Windows) == 0 {
		return ""
	}
	var parts []string
	for _, w := range q.Windows {
		if w.Display != "" {
			parts = append(parts, w.Name+" "+w.Display)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Name, w.Used))
	}
	return "  " + muted.Render(strings.Join(parts, " · "))
}
