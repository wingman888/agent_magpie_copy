// dial — one small dial for every coding agent's model.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/dial/internal/agent"
	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/profile"
	"github.com/yetone/dial/internal/tui"
)

var version = "dev"

const usage = `dial — one dial for every coding agent's model

  dial                          open the app: a window plus a menu bar icon
  dial tray                     start in the menu bar only
  dial tui                      the same dial, in the terminal
  dial ls                       list detected agents and their settings
  dial <agent>                  show one agent
  dial <agent> <model>          set an agent's model   e.g. dial claude deepseek/deepseek-chat
  dial <agent> <field> <value>  set another field   e.g. dial codex effort high
  dial <agent> [field] default  back to the agent's own default, dial's wiring removed

  dial save <name>              snapshot every agent's settings as a profile
  dial use <name>               apply a profile
  dial profiles                 list profiles
  dial rm <name>                delete a profile

  dial providers                list your providers: host, key, models, who uses them
  dial presets                  the vendors dial knows: add one with just a key
  dial provider add <preset> <key>   e.g. dial provider add deepseek sk-…
  dial provider add <name> k=v…      a custom vendor (dial provider for the fields)
  dial provider key|models|test|rm <id>
  dial models                   every model agents can pick, as provider/model

  dial serve                    run the gateway alone (the app runs it too)
  dial usage [today|7d|30d|all] tokens and cost per agent and model (30d)
  dial sync                     refresh the model catalog and vendor model lists
  dial agents                   list every supported agent

agents: claude (cc), codex, gemini, opencode (oc), pi, goose, cursor, copilot, crush
`

var (
	bold  = lipgloss.NewStyle().Bold(true)
	muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8B8F98", Dark: "#7C8290"})
	faint = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C4C7CE", Dark: "#4A4F5A"})
	green = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0F9D58", Dark: "#7EE2A8"})
)

func main() {
	gateway.Version = version
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		if hasGUI {
			return runGUI(true)
		}
		return tui.Run()
	}
	switch args[0] {
	case "tui":
		return tui.Run()
	case "app", "gui":
		return runGUI(true)
	case "tray":
		return runGUI(false)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-v", "--version", "version":
		fmt.Println("dial", version)
		return nil
	case "ls", "list":
		return list(agent.Detected(), true)
	case "agents":
		return list(agent.All(), false)
	case "sync":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := catalog.Sync(ctx); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "catalog saved to", catalog.CachePath())
		refreshLive(ctx)
		return nil
	case "save", "use", "rm", "profiles":
		return profiles(args)
	case "providers":
		return providers()
	case "presets":
		return presets()
	case "provider":
		return providerCmd(args)
	case "models":
		return models()
	case "serve":
		return serve()
	case "usage":
		return usageCmd(args)
	}

	a, err := agent.Find(args[0])
	if err != nil {
		return err
	}
	switch len(args) {
	case 1:
		return list([]*agent.Agent{a}, true)
	case 2:
		if args[1] == "default" {
			return set(a, a.Fields[0].Key, "")
		}
		// `dial codex xhigh`: a bare value that belongs to a non-model field
		// (effort levels, for instance) is routed there; anything else is a model.
		if f := fieldForValue(a, args[1]); f != nil {
			return set(a, f.Key, args[1])
		}
		return set(a, a.Fields[0].Key, args[1])
	case 3:
		if args[2] == "default" {
			args[2] = ""
		}
		return set(a, args[1], args[2])
	}
	return fmt.Errorf("too many arguments\n\n%s", usage)
}

func set(a *agent.Agent, key, value string) error {
	f := a.Field(key)
	if f == nil {
		var keys []string
		for _, f := range a.Fields {
			keys = append(keys, f.Key)
		}
		return fmt.Errorf("%s has no field %q (fields: %s)", a.Name, key, strings.Join(keys, ", "))
	}
	if err := f.Set(value); err != nil {
		return err
	}
	if value == "" {
		value = muted.Render("default")
	}
	fmt.Println(green.Render("✓"), bold.Render(a.Name), muted.Render(f.Label), value)
	if a.Notice != nil {
		if n := a.Notice(); n != "" {
			fmt.Println(muted.Render("  ↻ " + n))
		}
	}
	return nil
}

func fieldForValue(a *agent.Agent, v string) *agent.Field {
	vals := a.Values()
	for i := 1; i < len(a.Fields); i++ {
		for _, o := range a.Fields[i].Options(vals) {
			if o.Value == v {
				return &a.Fields[i]
			}
		}
	}
	return nil
}

func list(agents []*agent.Agent, detectedOnly bool) error {
	if len(agents) == 0 {
		return fmt.Errorf("no supported agents found on this machine")
	}
	type row struct{ name, vals, path string }
	var rows []row
	nameW, valW := 0, 0
	for _, a := range agents {
		r := row{name: a.Name, path: tilde(a.Path)}
		if !detectedOnly && !a.Detected() {
			r.name = faint.Render(a.Name)
			r.vals = faint.Render("not detected")
			r.path = ""
		} else {
			vals := a.Values()
			var parts []string
			for _, f := range a.Fields {
				v := vals[f.Key]
				if v == "" {
					v = faint.Render("—")
				}
				if f.Label == "model" {
					parts = append(parts, v)
				} else {
					parts = append(parts, muted.Render(f.Label)+" "+v)
				}
			}
			r.name = bold.Render(a.Name)
			r.vals = strings.Join(parts, muted.Render("  ·  "))
		}
		nameW = max(nameW, lipgloss.Width(r.name))
		valW = max(valW, lipgloss.Width(r.vals))
		rows = append(rows, r)
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s  %s\n", pad(r.name, nameW), pad(r.vals, valW), faint.Render(r.path))
	}
	return nil
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func profiles(args []string) error {
	switch args[0] {
	case "profiles":
		ps, err := profile.Load()
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			fmt.Println(muted.Render("no profiles yet · dial save <name>"))
			return nil
		}
		for _, n := range profile.Names(ps) {
			fmt.Printf("  %s  %s\n", bold.Render(n), muted.Render(profile.Summary(ps[n])))
		}
		return nil
	case "save":
		if len(args) < 2 {
			return fmt.Errorf("usage: dial save <name>")
		}
		if err := profile.Save(args[1], profile.Snapshot()); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved profile", bold.Render(args[1]))
		return nil
	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: dial use <name>")
		}
		ps, err := profile.Load()
		if err != nil {
			return err
		}
		p, ok := ps[args[1]]
		if !ok {
			return fmt.Errorf("no profile named %q", args[1])
		}
		n, err := profile.Apply(p)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "applied", bold.Render(args[1]), muted.Render(fmt.Sprintf("(%d changed)", n)))
		return nil
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: dial rm <name>")
		}
		if err := profile.Delete(args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "deleted profile", bold.Render(args[1]))
		return nil
	}
	return nil
}

func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
