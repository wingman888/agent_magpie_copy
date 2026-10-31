// magpie — one place to pick every agent's model.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/claudebridge"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/tui"
	"github.com/yetone/magpie/internal/update"
)

var version = "dev"

const usage = `magpie — one place to pick every agent's model

  magpie                          open the app: a window plus a menu bar icon
  magpie tray                     start in the menu bar only
  magpie tui                      the same thing, in the terminal
  magpie ls                       list detected agents and their settings
  magpie <agent>                  show one agent
  magpie <agent> <model>          set an agent's model   e.g. magpie claude deepseek/deepseek-chat
  magpie <agent> <field> <value>  set another field   e.g. magpie codex effort high
  magpie <agent> [field] default  back to the agent's own default, magpie's wiring removed

  magpie save <name>              snapshot every agent's settings as a profile
  magpie use <name>               apply a profile
  magpie profiles                 list profiles
  magpie rm <name>                delete a profile

  magpie backup [--no-keys] [file]    providers, keys, settings, profiles and agent models in one file, sealed with a passphrase
  magpie restore [--no-agents] <file> put a backup in on this machine

  magpie providers                list your providers: host, key, models, who uses them
  magpie presets                  the vendors magpie knows: add one with just a key
  magpie provider add <preset> <key>   e.g. magpie provider add deepseek sk-…
  magpie provider add <name> k=v…      a custom vendor (magpie provider for the fields)
  magpie provider key|models|test|rm <id>
  magpie provider fallback <id> <provider/model>…   use these when it's out of quota or down
  magpie import [-y] <link>       add the provider a magpie://import?… link describes
  magpie models                   every model agents can pick, as provider/model
  magpie accounts [agent] [--json]  every subscription magpie knows, with each one's allowance used and when it resets
  magpie accounts add <agent>     sign in to one more Claude or ChatGPT subscription
  magpie accounts switch <agent> <email>   sign the agent in to another of them

  magpie serve                    run the gateway alone (the app runs it too)
  magpie usage [today|7d|30d|all] tokens and cost per agent and model (30d)
  magpie sync                     refresh the model catalog and vendor model lists
  magpie agents                   list every supported agent
  magpie update [check]           install the newest release (check: only say if there is one)

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
	netproxy.Install()
	update.GUI = hasGUI
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "magpie:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// internal: the auth provider of a Grok run behind the gateway, asked
	// for a token often; nothing else of magpie's needs to start for it
	if len(args) == 3 && args[0] == "grok-token" {
		return provider.GrokToken(os.Stdout, args[1], args[2], os.Getenv("GROK_AUTH_EXPIRED") == "1")
	}
	settings.Migrate()
	agent.RenameLegacy()
	if len(args) == 0 {
		if hasGUI {
			return runGUI(true, "")
		}
		return tui.Run()
	}
	// a magpie:// link the system handed over (Windows, Linux): the app
	// opens it for the user to confirm
	if strings.HasPrefix(strings.ToLower(args[0]), "magpie:") {
		if !hasGUI {
			return importCmd(args)
		}
		return runGUI(false, args[0])
	}
	switch args[0] {
	case "tui":
		return tui.Run()
	case "app", "gui":
		return runGUI(true, "")
	case "tray":
		return runGUI(false, "")
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-v", "--version", "version":
		fmt.Println("magpie", version)
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
	case "import":
		return importCmd(args[1:])
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
	case "accounts", "account":
		return accountsCmd(args)
	case "usage":
		return usageCmd(args)
	case "update":
		return updateCmd(args)
	case "backup":
		return backupCmd(args[1:])
	case "restore":
		return restoreCmd(args[1:])
	case "claude-mcp-helper": // internal: stdio MCP subprocess spawned by Claude Code
		return claudebridge.RunMCP(args[1:])
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
		// `magpie codex xhigh`: a bare value that belongs to a non-model field
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
	// a model stays with the model, even where other fields offer it too
	// (Claude Code's opus/sonnet/haiku/fable)
	for _, o := range a.Fields[0].Options(vals) {
		if o.Value == v {
			return nil
		}
	}
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
				if v == "" && f.Quiet {
					continue
				}
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
			fmt.Println(muted.Render("no profiles yet · magpie save <name>"))
			return nil
		}
		for _, n := range profile.Names(ps) {
			fmt.Printf("  %s  %s\n", bold.Render(n), muted.Render(profile.Summary(ps[n])))
		}
		return nil
	case "save":
		if len(args) < 2 {
			return fmt.Errorf("usage: magpie save <name>")
		}
		if err := profile.Save(args[1], profile.Snapshot()); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "saved profile", bold.Render(args[1]))
		return nil
	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: magpie use <name>")
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
			return fmt.Errorf("usage: magpie rm <name>")
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
