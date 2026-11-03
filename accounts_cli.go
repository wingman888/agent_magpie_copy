package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// accountsCmd: `magpie accounts [agent]`, `magpie accounts add <agent>`,
// `magpie accounts switch|forget <agent> <email>`
// — the Claude Code and Codex subscriptions magpie remembers, and switching
// the agent between them.
func accountsCmd(args []string) error {
	const usage = "usage: magpie accounts [claude|codex] | magpie accounts add <claude|codex> | magpie accounts switch|forget <claude|codex> <email>"
	agentID := func(s string) (string, error) {
		switch strings.ToLower(s) {
		case "claude", "cc":
			return "claude", nil
		case "codex":
			return "codex", nil
		}
		return "", fmt.Errorf("%q: only Claude Code and Codex accounts can be switched\n%s", s, usage)
	}
	if len(args) > 1 && args[1] == "add" {
		if len(args) != 3 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		return addAccount(id)
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
		fmt.Println(muted.Render("no accounts yet ·"), "add one: magpie accounts add <claude|codex>")
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
	fmt.Println(faint.Render("  add one: magpie accounts add <agent> · switch: magpie accounts switch <agent> <email>"))
	return nil
}

// addAccount signs in to one more subscription in the browser, the way the
// window's "Add account" does.
func addAccount(agentID string) error {
	st, err := provider.StartSignIn(agentID)
	if err != nil {
		return err
	}
	fmt.Println("Finish signing in in your browser. If it didn't open, go to:")
	fmt.Println(faint.Render(st.URL))
	openInBrowser(st.URL)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err = provider.WaitSignIn(ctx, st.ID)
	if err != nil {
		return err
	}
	switch st.State {
	case "done":
		if st.Using {
			fmt.Println(green.Render("✓"), agentID, "is signed in as", st.User)
		} else {
			fmt.Println(green.Render("✓"), "added", st.User, muted.Render("· use it: magpie accounts switch "+agentID+" "+st.User))
		}
		return nil
	case "failed":
		return fmt.Errorf("sign-in didn't finish: %s", st.Error)
	}
	return fmt.Errorf("sign-in canceled")
}

func openInBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
