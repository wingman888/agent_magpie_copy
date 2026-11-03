package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
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
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, key, models, catalog, icon, header.<Name>
  magpie provider key <id> <key>          replace the current API key
  magpie provider key add|use|on|off|rm|rename|protocol …   keep several keys (magpie provider key for details)
  magpie provider icon <id> <file|name|"">  set a custom vendor's icon: a picture file, a built-in name, or "" for none
  magpie provider models <id> [ids…]      fetch the vendor's model list, or choose which models to expose
  magpie provider test <id>               send a tiny request through each endpoint
  magpie provider rm <id>                 remove a provider

  e.g. magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-…
       magpie provider add "Own Claude" anthropic=https://gw.example.com key=sk-… catalog=anthropic`

// providers: `magpie providers`
func providers() error {
	all := provider.All()
	if len(all) == 0 && len(provider.Excluded()) == 0 {
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
			if n := len(p.KeyList()) - 1; n > 0 {
				r.key += muted.Render(fmt.Sprintf(" +%d more", n))
			}
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
		back := ""
		if x.Provider != "" {
			back = " · magpie provider add " + x.Provider + " brings it back"
		}
		fmt.Println(" ", muted.Render(name+" is signed in but not offered: "+x.Why+back))
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
		return keyCmd(rest)
	case "icon":
		if len(rest) != 2 {
			return fmt.Errorf(`magpie provider icon <id> <picture file|built-in name|"">`)
		}
		p, err := provider.Find(rest[0])
		if err != nil {
			return err
		}
		if p.Preset != "" || p.Account != nil {
			return errors.New("only custom providers can have an icon (仅自定义供应商可设置图标)")
		}
		ref, err := iconArg(rest[1])
		if err != nil {
			return err
		}
		p.Icon = ref
		if err := provider.Save(*p); err != nil {
			return err
		}
		if ref == "" {
			fmt.Println(green.Render("✓"), p.Name, "icon", muted.Render("reset to the default"))
		} else {
			fmt.Println(green.Render("✓"), p.Name, "icon", muted.Render(ref))
		}
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
	return saveNew(p)
}

// saveNew saves a provider the user just added, then asks the vendor for
// its models.
func saveNew(p provider.Provider) error {
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
		printKeys(p)
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("not set")+muted.Render("  magpie provider key "+p.ID+" …"))
	}
	kv("catalog", p.Catalog)
	kv("website", p.Website)
	kv("keys", p.KeysURL)
	names := make([]string, 0, len(p.Headers))
	for name := range p.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		kv("header", name+": "+p.Headers[name])
	}
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

// keyVerbs are the `magpie provider key <verb> …` subcommands. Without one,
// `key <id> <key>` replaces the current key.
var keyVerbs = map[string]bool{"add": true, "use": true, "on": true, "off": true, "rm": true, "remove": true, "rename": true, "protocol": true}

const keyUsage = `usage:
  magpie provider key <id> <key>                          replace the current API key
  magpie provider key add <id> <key> [name=…] [protocol=chat|responses|anthropic|any]
  magpie provider key use <id> <key>                      make a key the current one
  magpie provider key on|off <id> <key>                   enable or disable a key
  magpie provider key rm <id> <key>                       delete a key
  magpie provider key rename <id> <key> <name…>           name a key
  magpie provider key protocol <id> <key> <chat|responses|anthropic|any>
  <key> is a key's name or its fingerprint; magpie provider <id> lists them`

// keyCmd: `magpie provider key …`
func keyCmd(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("%s", keyUsage)
	}
	if !keyVerbs[rest[0]] {
		if len(rest) != 2 {
			return fmt.Errorf("%s", keyUsage)
		}
		return replaceKey(rest[0], rest[1])
	}
	verb, args := rest[0], rest[1:]
	if verb == "add" {
		return addKey(args)
	}
	if len(args) < 2 {
		return fmt.Errorf("%s", keyUsage)
	}
	p, err := provider.Find(args[0])
	if err != nil {
		return err
	}
	ref, err := p.ResolveKey(args[1])
	if err != nil {
		return err
	}
	label := keyLabel(*p, ref)
	switch verb {
	case "use":
		if len(args) != 2 {
			return fmt.Errorf("%s", keyUsage)
		}
		if err := provider.UseKey(p.ID, ref); err != nil {
			return err
		}
		return reportKeys(p.ID, label+" is now the current key")
	case "on", "off":
		if len(args) != 2 {
			return fmt.Errorf("%s", keyUsage)
		}
		if err := provider.SetKeyOn(p.ID, ref, verb == "on"); err != nil {
			return err
		}
		return reportKeys(p.ID, label+" is "+verb)
	case "rm", "remove":
		if len(args) != 2 {
			return fmt.Errorf("%s", keyUsage)
		}
		if err := provider.RemoveKey(p.ID, ref); err != nil {
			return err
		}
		return reportKeys(p.ID, "removed "+label)
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("%s", keyUsage)
		}
		name := strings.Join(args[2:], " ")
		if err := provider.RenameKey(p.ID, ref, name); err != nil {
			return err
		}
		return reportKeys(p.ID, label+" is now called "+name)
	case "protocol":
		if len(args) != 3 {
			return fmt.Errorf("%s", keyUsage)
		}
		proto, err := keyProtocolArg(args[2])
		if err != nil {
			return err
		}
		if err := provider.SetKeyProtocol(p.ID, ref, proto); err != nil {
			return err
		}
		return reportKeys(p.ID, label+" is limited to "+protoLabel(proto))
	}
	return fmt.Errorf("%s", keyUsage)
}

// replaceKey: `magpie provider key <id> <key>` swaps the current key. The
// new key takes no name or protocol limit from the one it replaces.
func replaceKey(id, key string) error {
	p, err := provider.Find(id)
	if err != nil {
		return err
	}
	if p.Key != key {
		p.KeyName, p.KeyProtocol = "", ""
	}
	p.Key = key
	if err := provider.Save(*p); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(p.Key)))
	return nil
}

// addKey: `magpie provider key add <id> <key> [name=…] [protocol=…]`
func addKey(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", keyUsage)
	}
	var name string
	var proto provider.Protocol
	for _, kv := range args[2:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected name=… or protocol=…, got %q\n\n%s", kv, keyUsage)
		}
		switch strings.ToLower(k) {
		case "name":
			name = v
		case "protocol":
			pr, err := keyProtocolArg(v)
			if err != nil {
				return err
			}
			proto = pr
		default:
			return fmt.Errorf("unknown field %q\n\n%s", k, keyUsage)
		}
	}
	if err := provider.AddKey(args[0], name, args[1], proto); err != nil {
		return err
	}
	return reportKeys(args[0], "added key "+provider.Mask(args[1]))
}

// keyProtocolArg reads a protocol the user typed; "any" clears the limit.
func keyProtocolArg(v string) (provider.Protocol, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "any" {
		v = ""
	}
	proto := provider.Protocol(v)
	if !provider.ValidKeyProtocol(proto) {
		return "", fmt.Errorf("%q is not a protocol: use chat, responses, anthropic or any", v)
	}
	return proto, nil
}

// protoLabel names a key's protocol limit for display.
func protoLabel(proto provider.Protocol) string {
	if proto == "" {
		return "any protocol"
	}
	return string(proto) + " only"
}

// keyLabel is how a key is named back to the user: its name, or its
// fingerprint when it has none.
func keyLabel(p provider.Provider, ref string) string {
	for _, k := range p.KeyList() {
		if k.Ref == ref && k.Name != "" {
			return k.Name
		}
	}
	return ref
}

// reportKeys confirms a change and lists the provider's keys as they now are.
func reportKeys(id, msg string) error {
	p, err := provider.Find(id)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), p.Name, "·", msg)
	printKeys(*p)
	return nil
}

// printKeys lists a provider's keys, the current one first.
func printKeys(p provider.Provider) {
	for i, k := range p.KeyList() {
		lead := ""
		if i == 0 {
			lead = "key"
		}
		state := green.Render("● on")
		switch {
		case k.Current:
			state = green.Render("● current")
		case k.Off:
			state = amber.Render("○ off")
		}
		name := k.Name
		if name == "" {
			name = faint.Render("unnamed")
		}
		fmt.Printf("  %s %s  %s  %s  %s  %s\n", muted.Render(pad(lead, 10)), state, name, muted.Render(k.Ref), muted.Render(k.Masked), muted.Render(protoLabel(k.Protocol)))
	}
}

// iconArg turns what the user typed as an icon into the value to store. A
// picture file is copied into magpie's icon store, a built-in name is kept,
// and "" clears the icon.
func iconArg(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if info, err := os.Stat(v); err == nil && !info.IsDir() {
		if info.Size() > provider.MaxIcon {
			return "", fmt.Errorf("%s is larger than %d KB", v, provider.MaxIcon>>10)
		}
		data, err := os.ReadFile(v)
		if err != nil {
			return "", err
		}
		return provider.StoreIcon(data)
	}
	if provider.BuiltinIcon(v) {
		return v, nil
	}
	return "", fmt.Errorf("%q is neither a picture file nor a built-in icon name", v)
}

// validHeaderName is an HTTP token: what a header name may be made of.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

func applyPairs(p *provider.Provider, pairs []string) error {
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q\n\n%s", kv, providerUsage)
		}
		if len(k) > len("header.") && strings.EqualFold(k[:len("header.")], "header.") {
			// header.<Name>=value: the name keeps the case typed.
			name := k[len("header."):]
			if !validHeaderName(name) {
				return fmt.Errorf("%q is not a valid header name\n\n%s", name, providerUsage)
			}
			if strings.ContainsAny(v, "\r\n") {
				return fmt.Errorf("header %s has a line break in its value", name)
			}
			if p.Headers == nil {
				p.Headers = map[string]string{}
			}
			p.Headers[name] = v
			continue
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
			if p.Preset != "" {
				return errors.New("only custom providers can have an icon (仅自定义供应商可设置图标)")
			}
			ref, err := iconArg(v)
			if err != nil {
				return err
			}
			p.Icon = ref
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

// importCmd adds the provider a magpie://import link describes, after
// showing it: magpie import [-y] <link>.
func importCmd(args []string) error {
	yes := false
	var link string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		default:
			link = a
		}
	}
	if link == "" {
		return errors.New("magpie import [-y] 'magpie://import?…'")
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	if err := showProvider(p); err != nil {
		return err
	}
	if old, err := provider.Find(p.ID); err == nil {
		fmt.Println(amber.Render("!"), "replaces your", old.Name)
	}
	if !yes {
		if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return errors.New("not a terminal: add -y to import without asking")
		}
		fmt.Print("Add it? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("nothing added")
		}
	}
	if p.Key == "" && !p.Ready() {
		fmt.Println(amber.Render("!"), "the link has no key · magpie provider key", p.ID, "<key>")
	}
	return saveNew(p)
}
