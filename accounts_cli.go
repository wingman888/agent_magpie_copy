package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// accountsCmd: `magpie accounts [agent] [--json]`, `magpie accounts add <agent>`,
// `magpie accounts switch|forget <agent> <email>`
// — the subscriptions magpie remembers, how much of each one's allowance is
// used, and switching the agent between them.
func accountsCmd(args []string) error {
	const usage = "usage: magpie accounts [claude|codex|grok|copilot] [--json] | magpie accounts add <claude|codex> | magpie accounts switch|forget <claude|codex> <email>"
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
	which, asJSON := "", false
	for _, a := range args[1:] {
		switch strings.ToLower(a) {
		case "--json":
			asJSON = true
		case "grok", "copilot":
			which = strings.ToLower(a)
		default:
			id, err := agentID(a)
			if err != nil {
				return err
			}
			which = id
		}
	}
	ls := provider.Logins(which)
	rows := accountRows(ls, time.Now())
	if asJSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(ls) == 0 {
		fmt.Println(muted.Render("no accounts yet ·"), "add one: magpie accounts add <claude|codex>")
		return nil
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r.User))
	}
	for _, r := range rows {
		mark := "  "
		switch {
		case r.Active:
			mark = green.Render("● ")
		case r.On:
			mark = muted.Render("○ ")
		}
		plan := ""
		if r.Plan != "" {
			plan = " · " + r.Plan
		}
		line := fmt.Sprintf("%s%-7s %-*s %s", mark, r.Agent, width, r.User, muted.Render(fmt.Sprintf("%-8s", plan)))
		for _, w := range r.Windows {
			line += "  " + quotaCell(w)
		}
		if r.Error != "" {
			line += "  " + muted.Render(r.Error)
		}
		fmt.Println(line)
	}
	fmt.Println(faint.Render("  ● signed in · ○ takes over when it runs out · add one: magpie accounts add <agent> · switch: magpie accounts switch <agent> <email>"))
	return nil
}

// accountRow is one account and its allowance, as `magpie accounts` shows it.
type accountRow struct {
	Agent   string      `json:"agent"`
	User    string      `json:"user"`
	Plan    string      `json:"plan,omitempty"`
	Active  bool        `json:"active"` // the agent is signed in to it
	On      bool        `json:"on"`     // in use: the active one, or next in line
	Windows []quotaSpan `json:"windows"`
	Error   string      `json:"error,omitempty"`
}

type quotaSpan struct {
	Name      string     `json:"name"`
	Used      float64    `json:"used"`      // percent
	Remaining float64    `json:"remaining"` // percent
	ResetsAt  *time.Time `json:"resetsAt,omitempty"`
	Display   string     `json:"display,omitempty"`
}

// accountRows asks each agent's accounts for their allowance at once; what
// was asked less than a minute ago comes from magpie's cache.
func accountRows(ls []provider.Login, now time.Time) []accountRow {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage := map[string]map[string]provider.SubscriptionQuota{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range ls {
		if _, ok := usage[l.Agent]; ok {
			continue
		}
		usage[l.Agent] = nil
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			u := provider.LoginUsage(ctx, agent)
			mu.Lock()
			usage[agent] = u
			mu.Unlock()
		}(l.Agent)
	}
	wg.Wait()
	rows := []accountRow{}
	for _, l := range ls {
		r := accountRow{Agent: l.Agent, User: l.User, Plan: l.Plan, Active: l.Active, On: l.On, Windows: []quotaSpan{}}
		for user, q := range usage[l.Agent] {
			if !strings.EqualFold(user, l.User) {
				continue
			}
			if r.Plan == "" {
				r.Plan = q.Plan
			}
			r.Error = q.Error
			for _, w := range q.Windows {
				s := quotaSpan{Name: w.Name, Used: w.Used, Remaining: max(0, 100-w.Used), ResetsAt: w.ResetsAt, Display: w.Display}
				if s.ResetsAt == nil && w.ResetSecs > 0 {
					t := now.Add(time.Duration(w.ResetSecs) * time.Second)
					s.ResetsAt = &t
				}
				r.Windows = append(r.Windows, s)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// quotaCell is one window in a line: "5h 42% ↻2h13m".
func quotaCell(w quotaSpan) string {
	name := strings.NewReplacer(" hours", "h", " hour", "h", " days", "d", " day", "d", " · ", " ").Replace(w.Name)
	cell := fmt.Sprintf("%s %.0f%%", name, w.Used)
	if w.Display != "" {
		cell += " (" + w.Display + ")"
	}
	if w.ResetsAt != nil {
		cell += muted.Render(" ↻" + untilShort(time.Until(*w.ResetsAt)))
	}
	return cell
}

// untilShort is a time left as "45m", "2h13m" or "3d4h".
func untilShort(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
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
