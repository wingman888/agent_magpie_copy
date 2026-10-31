package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// accountsCmd: `magpie accounts [agent]`, `magpie accounts switch|forget <agent> <email>`
// — the Claude Code and Codex subscriptions magpie remembers, and switching
// the agent between them.
func accountsCmd(args []string) error {
	const usage = "usage: magpie accounts [claude|codex] | magpie accounts switch|forget <claude|codex> <email>"
	agentID := func(s string) (string, error) {
		switch strings.ToLower(s) {
		case "claude", "cc":
			return "claude", nil
		case "codex":
			return "codex", nil
		}
		return "", fmt.Errorf("%q: only Claude Code and Codex accounts can be switched\n%s", s, usage)
	}
	if len(args) > 1 && (args[1] == "switch" || args[1] == "forget") {
		if len(args) != 4 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		if args[1] == "forget" {
			if err := provider.ForgetLogin(id, args[3]); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", args[3])
			return nil
		}
		if err := provider.SwitchLogin(id, args[3]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), id, "is now signed in as", args[3], muted.Render("· sessions already running keep their account until restarted"))
		return nil
	}
	which := ""
	if len(args) > 1 {
		id, err := agentID(args[1])
		if err != nil {
			return err
		}
		which = id
	}
	ls := provider.Logins(which)
	if len(ls) == 0 {
		fmt.Println(muted.Render("no accounts yet ·"), "sign in to Claude Code or Codex and magpie remembers the account")
		return nil
	}
	for _, l := range ls {
		mark := "  "
		if l.Active {
			mark = green.Render("● ")
		}
		plan := ""
		if l.Plan != "" {
			plan = muted.Render(" · " + l.Plan)
		}
		fmt.Printf("%s%-7s %s%s\n", mark, l.Agent, l.User, plan)
	}
	fmt.Println(faint.Render("  add one: sign in to it in the agent (codex login · claude → /login); switch: magpie accounts switch <agent> <email>"))
	return nil
}
