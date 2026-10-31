package main

import (
	"fmt"
	"strings"

	"github.com/yetone/dial/internal/agent"
	stats "github.com/yetone/dial/internal/usage"
)

// usageCmd: `dial usage [today|7d|30d|all]` — tokens and cost per agent and model
func usageCmd(args []string) error {
	period := stats.Month
	if len(args) > 1 {
		switch strings.ToLower(args[1]) {
		case "today", "day":
			period = stats.Today
		case "7d", "week":
			period = stats.Week
		case "30d", "month":
			period = stats.Month
		case "all":
			period = stats.All
		default:
			return fmt.Errorf("usage: dial usage [today|7d|30d|all]")
		}
	}
	s := stats.Summarize(period)
	title := map[stats.Period]string{stats.Today: "today", stats.Week: "last 7 days", stats.Month: "last 30 days", stats.All: "all time"}[s.Period]
	if s.Calls == 0 {
		fmt.Println(muted.Render("no calls "+title+" ·"), "route an agent through dial and its usage shows up here")
		fmt.Println(faint.Render("  " + stats.Path()))
		return nil
	}
	fmt.Println(bold.Render(fmtTokens(s.Tokens())+" tokens"), muted.Render(title+" ·"), plural(s.Calls, "call"), muted.Render("·"), cost(s.Totals))
	fmt.Println(muted.Render("  in "+fmtTokens(s.Input)+"  out "+fmtTokens(s.Output)+"  cache read "+fmtTokens(s.CacheRead)+"  cache write "+fmtTokens(s.CacheWrite)+"  reasoning "+fmtTokens(s.Reasoning)),
		func() string {
			if s.Errors > 0 {
				return muted.Render(" · ") + plural(s.Errors, "error")
			}
			return ""
		}())

	names := map[string]string{}
	for _, a := range agent.All() {
		names[a.ID] = a.Name
	}
	table := func(head string, gs []stats.Group, name func(stats.Group) string) {
		fmt.Println()
		fmt.Println(faint.Render("  " + head))
		w := 0
		for _, g := range gs {
			w = max(w, len(name(g)))
		}
		for _, g := range gs {
			share := ""
			if s.Tokens() > 0 {
				share = fmt.Sprintf("%3.0f%%", 100*float64(g.Tokens())/float64(s.Tokens()))
			}
			fmt.Println("  "+pad(name(g), w), muted.Render(share), pad(fmtTokens(g.Tokens()), 7), faint.Render(pad(plural(g.Calls, "call"), 10)), cost(g.Totals))
		}
	}
	table("agents", s.Agents, func(g stats.Group) string {
		if n := names[g.ID]; n != "" {
			return n
		}
		return g.ID
	})
	table("models", s.Models, func(g stats.Group) string { return g.ID })
	fmt.Println(faint.Render("  " + stats.Path()))
	return nil
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 100_000:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// cost renders list-price cost, saying when some calls could not be priced.
func cost(t stats.Totals) string {
	if t.Cost == 0 && t.Unpriced > 0 {
		return faint.Render("no price")
	}
	var s string
	switch {
	case t.Cost >= 100:
		s = fmt.Sprintf("$%.0f", t.Cost)
	case t.Cost >= 1:
		s = fmt.Sprintf("$%.2f", t.Cost)
	default:
		s = fmt.Sprintf("$%.3f", t.Cost)
	}
	if t.Unpriced > 0 {
		s += muted.Render("+")
	}
	return green.Render("≈" + s)
}
