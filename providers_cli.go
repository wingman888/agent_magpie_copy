package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

var amber = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F2B544"})

const providerUsage = `usage:
  magpie providers                        list providers, keys and who uses them
  magpie presets                          list the vendors magpie knows out of the box
  magpie provider <id>                    show one provider and its models
  magpie provider add <preset> <key>      add a preset vendor   e.g. magpie provider add deepseek sk-…
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, key, models, catalog
  magpie provider key <id> <key>          change the API key
  magpie provider models <id> [ids…]      fetch the vendor's model list, or choose which models to expose
  magpie provider test <id>               send a tiny request through each endpoint
  magpie provider rm <id>                 remove a provider

  e.g. magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-…
       magpie provider add "Own Claude" anthropic=https://gw.example.com key=sk-… catalog=anthropic`

// providers: `magpie providers`
func providers() error {
	all := provider.All()
	if len(all) == 0 {
		fmt.Println(muted.Render("no providers yet ·"), "magpie provider add deepseek sk-…", muted.Render("· magpie presets lists the vendors"))
		return nil
	}
	uses := usesByProvider()
	type row struct{ name, id, host, key, models, uses string }
	var rows []row
	w := [5]int{}
	for _, p := range all {
		r := row{name: bold.Render(p.Name), id: muted.Render(p.ID), host: p.Host()}
		if p.Preset == "" && p.Account == nil {
			r.name += " " + faint.Render("custom")
		}
		switch {
		case p.Account != nil:
			r.key = green.Render("●") + " " + muted.Render("signed in as "+p.Account.User)
		case p.Key != "":
			r.key = green.Render("●") + " " + muted.Render(provider.Mask(p.Key))
		case p.Ready():
			r.key = green.Render("●") + " " + muted.Render("no key needed")
		default:
			r.key = amber.Render("○ no key")
		}
		n := len(p.Exposed())
		if t, ok := p.Fetched(); ok {
			r.models = fmt.Sprintf("%d of %d models", n, len(p.Available())) + muted.Render(" · fetched "+ago(t))
		} else {
			r.models = fmt.Sprintf("%d models", n)
		}
		if u := uses[p.ID]; len(u) > 0 {
			r.uses = green.Render("← " + strings.Join(u, ", "))
		}
		for i, s := range []string{r.name, r.id, r.host, r.key, r.models} {
			w[i] = max(w[i], lipgloss.Width(s))
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s  %s  %s  %s  %s\n", pad(r.name, w[0]), pad(r.id, w[1]), pad(r.host, w[2]), pad(r.key, w[3]), pad(r.models, w[4]), r.uses)
	}
	for _, x := range provider.Excluded() {
		name := x.Agent
		if a, err := agent.Find(x.Agent); err == nil {
			name = a.Name
		}
		fmt.Println()
		fmt.Println(" ", muted.Render(name+" is signed in but not offered: "+x.Why))
	}
	return nil
}

// usesByProvider maps provider ids to the agents currently routed to them.
func usesByProvider() map[string][]string {
	out := map[string][]string{}
	for _, a := range agent.Detected() {
		if len(a.Fields) == 0 {
			continue
		}
		v := a.Fields[0].Get()
		v = strings.TrimPrefix(v, "magpie/")
		if pid, _, ok := strings.Cut(v, "/"); ok {
			out[pid] = append(out[pid], a.Name)
		}
	}
	return out
}

// presets: `magpie presets`
func presets() error {
	have := map[string]bool{}
	for _, p := range provider.All() {
		have[p.ID] = true
	}
	kind := provider.Kind("")
	for _, pr := range provider.Presets() {
		if pr.Kind != kind {
			kind = pr.Kind
			fmt.Println(faint.Render("  " + map[provider.Kind]string{provider.KindVendor: "vendors", provider.KindRelay: "relays", provider.KindLocal: "local"}[kind]))
		}
		name := bold.Render(pr.Name)
		if pr.Sponsored {
			name += " " + faint.Render("sponsored")
		}
		state := muted.Render("magpie provider add " + pr.ID + " <key>")
		if pr.NoKey {
			state = muted.Render("magpie provider add " + pr.ID)
		}
		if have[pr.ID] {
			state = green.Render("✓ added")
		}
		fmt.Printf("  %s  %s  %s\n", pad(name, 28), pad(muted.Render(pr.ID), 14), state)
	}
	return nil
}

// models: `magpie models` — the catalog every agent sees
func models() error {
	entries := provider.Catalog()
	if len(entries) == 0 {
		fmt.Println(muted.Render("no models yet · add a provider first:"), "magpie provider add deepseek sk-…")
		return nil
	}
	w := 0
	for _, e := range entries {
		w = max(w, len(e.ID))
	}
	last := ""
	for _, e := range entries {
		if e.Provider.ID != last {
			last = e.Provider.ID
			fmt.Println(faint.Render("  " + e.Provider.Name))
		}
		line := "  " + pad(e.ID, w)
		if e.Name != "" && e.Name != e.Model {
			line += "  " + muted.Render(e.Name)
		}
		if len(e.Efforts) > 0 {
			line += faint.Render("  " + strings.Join(e.Efforts, "/"))
		}
		fmt.Println(line)
	}
	fmt.Println(faint.Render("  " + gateway.URL() + "/v1"))
	return nil
}

// providerCmd: `magpie provider <verb> …`
func providerCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", providerUsage)
	}
	verb, rest := args[1], args[2:]
	switch verb {
	case "add":
		return addProvider(rest)
	case "key":
		if len(rest) != 2 {
			return fmt.Errorf("magpie provider key <id> <key>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		p.Key = rest[1]
		if err := provider.Save(*p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(p.Key)))
		return nil
	case "rm", "remove", "delete":
		if len(rest) != 1 {
			return fmt.Errorf("magpie provider rm <id>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if err := provider.Delete(p.ID); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed", p.Name)
		return nil
	case "test":
		if len(rest) != 1 {
			return fmt.Errorf("magpie provider test <id>")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ok := true
		for _, r := range p.Test(ctx) {
			if r.OK {
				fmt.Printf("  %s %-10s %s\n", green.Render("✓"), r.Protocol, muted.Render(fmt.Sprintf("%d ms · %s", r.Millis, r.Model)))
			} else {
				ok = false
				msg := r.Error
				if r.Status != 0 {
					msg = fmt.Sprintf("%d · %s", r.Status, r.Error)
				}
				fmt.Printf("  %s %-10s %s\n", amber.Render("✗"), r.Protocol, msg)
			}
		}
		if !ok {
			os.Exit(1)
		}
		return nil
	case "models":
		if len(rest) < 1 {
			return fmt.Errorf("magpie provider models <id> [model ids to expose…]")
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if len(rest) > 1 {
			p.Models = rest[1:]
			if len(rest) == 2 && (rest[1] == "-" || rest[1] == "all") {
				p.Models = nil
			}
			if err := provider.Save(*p); err != nil {
				return err
			}
			return showProvider(*p)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, err := p.Fetch(ctx)
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), len(ms), "models from", p.Host())
		return showProvider(*p)
	}
	// `magpie provider <id>`
	p, err := provider.Find(verb)
	if err != nil {
		return err
	}
	return showProvider(*p)
}

// addProvider: `magpie provider add <preset> [key]` or `magpie provider add <name> k=v…`
func addProvider(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("magpie provider add <preset> <key>   or   magpie provider add <name> k=v…\n\n%s", providerUsage)
	}
	var p provider.Provider
	if pr, err := provider.FromPreset(strings.ToLower(rest[0])); err == nil {
		p = pr
		if len(rest) > 1 && !strings.Contains(rest[1], "=") {
			p.Key = rest[1]
			rest = rest[2:]
		} else {
			rest = rest[1:]
		}
	} else {
		p = provider.Provider{Name: rest[0]}
		rest = rest[1:]
	}
	if err := applyPairs(&p, rest); err != nil {
		return err
	}
	if err := provider.Save(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		saved, err = provider.Find(p.Name)
	}
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
	}
	n := len(saved.Exposed())
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}

func showProvider(p provider.Provider) error {
	kv := func(k, v string) {
		if v != "" {
			fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
		}
	}
	name := bold.Render(p.Name) + muted.Render("  "+p.ID)
	if p.Preset == "" && p.Account == nil {
		name += faint.Render("  custom")
	}
	fmt.Println(" ", name)
	kv("chat", p.Chat)
	kv("responses", p.Responses)
	kv("anthropic", p.Anthropic)
	switch {
	case p.Account != nil:
		who := p.Account.User
		if p.Account.Plan != "" {
			who += muted.Render("  " + p.Account.Plan)
		}
		kv("account", who+muted.Render("  from "+p.Account.Agent+"'s own sign-in"))
	case p.Key != "":
		kv("key", muted.Render(provider.Mask(p.Key)))
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("not set")+muted.Render("  magpie provider key "+p.ID+" …"))
	}
	kv("catalog", p.Catalog)
	kv("website", p.Website)
	kv("keys", p.KeysURL)
	ms := p.Exposed()
	src := "models.dev"
	if t, ok := p.Fetched(); ok {
		src = p.Host() + " · fetched " + ago(t)
	}
	kv("models", fmt.Sprintf("%d exposed of %d %s", len(ms), len(p.Available()), muted.Render("from "+src)))
	for i, m := range ms {
		if i == 12 {
			fmt.Println(faint.Render(fmt.Sprintf("             … %d more", len(ms)-12)))
			break
		}
		line := "             " + p.ID + "/" + m.ID
		if m.Name != "" && m.Name != m.ID {
			line += muted.Render("  " + m.Name)
		}
		fmt.Println(line)
	}
	if u := usesByProvider()[p.ID]; len(u) > 0 {
		kv("used by", green.Render(strings.Join(u, ", ")))
	}
	return nil
}

func applyPairs(p *provider.Provider, pairs []string) error {
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q\n\n%s", kv, providerUsage)
		}
		switch strings.ToLower(k) {
		case "id":
			p.ID = v
		case "name":
			p.Name = v
		case "url", "chat", "openai":
			p.Chat = v
		case "responses":
			p.Responses = v
		case "anthropic":
			p.Anthropic = v
		case "key":
			p.Key = v
		case "catalog":
			p.Catalog = v
		case "models":
			p.Models = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		case "website":
			p.Website = v
		case "keys":
			p.KeysURL = v
		case "icon":
			p.Icon = v
		default:
			return fmt.Errorf("unknown field %q\n\n%s", k, providerUsage)
		}
	}
	return nil
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

// refreshLive re-fetches the model list of every ready provider; `magpie sync`
// calls it after the catalog download.
func refreshLive(ctx context.Context) {
	var names []string
	for _, p := range provider.All() {
		if !p.Ready() {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		ms, err := p.Fetch(c)
		cancel()
		if err == nil {
			names = append(names, fmt.Sprintf("%s (%d)", p.ID, len(ms)))
		}
	}
	if len(names) > 0 {
		fmt.Println(green.Render("✓"), "model lists:", strings.Join(names, ", "))
	}
}

// serve: `magpie serve` — the gateway alone, in the foreground.
func serve() error {
	s := gateway.New()
	fmt.Println(green.Render("●"), "magpie gateway on", bold.Render(gateway.URL()))
	fmt.Println(muted.Render("  OpenAI  "), gateway.URL()+"/v1/chat/completions", muted.Render("·"), gateway.URL()+"/v1/responses")
	fmt.Println(muted.Render("  Anthropic"), gateway.URL()+"/v1/messages")
	fmt.Println(muted.Render("  key     "), gateway.Token, muted.Render("(anything works; the gateway only listens on localhost)"))
	n := len(provider.Catalog())
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models yet ·", "magpie provider add deepseek sk-…")
	} else {
		fmt.Printf("  %d models · %s\n", n, muted.Render("magpie models"))
	}
	return s.ListenAndServe(context.Background())
}
