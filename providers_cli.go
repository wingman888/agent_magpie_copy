package main

import (
	"context"
	"errors"
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

const headerPrefix = "header."

const providerUsage = `usage:
  magpie providers                        list providers, keys and who uses them
  magpie presets                          list the vendors magpie knows out of the box
  magpie provider <id>                    show one provider and its models
  magpie provider add <preset> <key>      add a preset vendor   e.g. magpie provider add deepseek sk-…
  magpie provider add <name> k=v…         add a custom vendor   k: url, anthropic, responses, key, models, catalog, icon, header.<Name>=value
  magpie provider key <id> <key>          change the current API key
  magpie provider key <id> add <name> <key> [chat|responses|anthropic|any]   add another key, on after the others
  magpie provider key <id> use|on|off|rm <key>   make a key current, switch it on or off, or remove it
  magpie provider key <id> rename <key> <name>   name a key
  magpie provider key <id> protocol <key> chat|responses|anthropic|any   limit a key to one protocol
                                          (<key> is a key's fingerprint or its name)
  magpie provider icon <id> <file|name|"">  set a custom vendor's icon: an image file, a built-in icon name, or "" for the generic one
  magpie provider fallback <id> [provider/model …]   show or set the fallback models, tried in order
                                          when this provider is out of quota, rate limited or down ("-" clears)
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
		return iconCmd(rest)
	case "fallback":
		return fallbackCmd(rest)
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

// keyCmd handles `magpie provider key …`. A bare key replaces the current
// key; the other verbs work on keys named by fingerprint or by name.
func keyCmd(rest []string) error {
	usage := fmt.Errorf("usage:\n  magpie provider key <id> <key>\n  magpie provider key <id> add <name> <key> [chat|responses|anthropic|any]\n  magpie provider key <id> use|on|off|rm|rename|protocol <key> …")
	if len(rest) < 2 {
		return usage
	}
	id, verb, args := rest[0], strings.ToLower(rest[1]), rest[2:]
	switch verb {
	case "add", "use", "on", "off", "rm", "remove", "rename", "protocol":
	default:
		if len(rest) != 2 {
			return usage
		}
		p, err := provider.Find(id)
		if err != nil {
			return err
		}
		p.Key = rest[1]
		if err := provider.Save(*p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(p.Key)))
		return nil
	}
	p, err := provider.Find(id)
	if err != nil {
		return err
	}
	switch verb {
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return usage
		}
		proto := provider.Protocol("")
		if len(args) == 3 {
			proto = keyProtoArg(args[2])
		}
		if err := provider.AddKey(p.ID, args[0], args[1], proto); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "added key", muted.Render(provider.Mask(args[1])), "to", p.Name)
	case "use", "on", "off", "rm", "remove":
		if len(args) != 1 {
			return usage
		}
		ref, err := keyRefArg(p, args[0])
		if err != nil {
			return err
		}
		switch verb {
		case "use":
			err = provider.UseKey(p.ID, ref)
		case "on", "off":
			err = provider.SetKeyOn(p.ID, ref, verb == "on")
		default:
			err = provider.RemoveKey(p.ID, ref)
		}
		if err != nil {
			return err
		}
	case "rename":
		if len(args) != 2 {
			return usage
		}
		ref, err := keyRefArg(p, args[0])
		if err != nil {
			return err
		}
		if err := provider.RenameKey(p.ID, ref, args[1]); err != nil {
			return err
		}
	case "protocol":
		if len(args) != 2 {
			return usage
		}
		ref, err := keyRefArg(p, args[0])
		if err != nil {
			return err
		}
		if err := provider.SetKeyProtocol(p.ID, ref, keyProtoArg(args[1])); err != nil {
			return err
		}
	}
	p2, err := provider.Find(id)
	if err != nil {
		return err
	}
	return showProvider(*p2)
}

// keyProtoArg reads a protocol the user typed; "any" lifts the limit.
func keyProtoArg(s string) provider.Protocol {
	if strings.EqualFold(s, "any") {
		return ""
	}
	return provider.Protocol(strings.ToLower(s))
}

// keyRefArg turns a key's fingerprint, or its name, into the fingerprint.
func keyRefArg(p *provider.Provider, arg string) (string, error) {
	type entry struct{ key, name string }
	var all []entry
	if p.Key != "" {
		all = append(all, entry{p.Key, p.KeyName})
	}
	for _, k := range p.Keys {
		all = append(all, entry{k.Key, k.Name})
	}
	arg = strings.TrimSpace(arg)
	for _, e := range all {
		if provider.KeyRef(e.key) == strings.ToLower(arg) {
			return provider.KeyRef(e.key), nil
		}
	}
	var hits []string
	for _, e := range all {
		if e.name != "" && strings.EqualFold(e.name, arg) {
			hits = append(hits, provider.KeyRef(e.key))
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%s has no key %q; magpie provider %s lists them", p.Name, arg, p.ID)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("more than one key of %s is named %q; use its fingerprint", p.Name, arg)
}

// showKeys lists a provider's keys when it has more than the current one:
// the current key first, then the others in the order they are tried.
func showKeys(p provider.Provider) {
	if p.Account != nil || p.Key == "" || (len(p.Keys) == 0 && p.KeyName == "" && p.KeyProtocol == "") {
		return
	}
	proto := func(pr provider.Protocol) string {
		if pr == "" {
			return "any"
		}
		return string(pr)
	}
	row := func(key, name string, pr provider.Protocol, off, current bool) {
		state := green.Render("on")
		if off {
			state = faint.Render("off")
		}
		if name == "" {
			name = "(unnamed)"
		}
		mark := ""
		if current {
			mark = muted.Render("  current")
		}
		fmt.Printf("             %s  %-16s %-10s %s%s\n", provider.KeyRef(key), name, proto(pr), state, mark)
	}
	fmt.Println(" ", muted.Render(pad("keys", 10)))
	row(p.Key, p.KeyName, p.KeyProtocol, false, true)
	for _, k := range p.Keys {
		row(k.Key, k.Name, k.Protocol, k.Off, false)
	}
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
	if p.Preset != "" && hasPairKey(rest, "icon") {
		return fmt.Errorf("%s has its own icon; only custom providers can set one", p.Name)
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
		kv("key", muted.Render(provider.Mask(p.Key)))
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("not set")+muted.Render("  magpie provider key "+p.ID+" …"))
	}
	showKeys(p)
	for _, h := range p.ExtraHeaders() {
		kv("header", h[0]+": "+h[1])
	}
	if p.Preset == "" && p.Account == nil {
		kv("icon", p.Icon)
	}
	kv("catalog", p.Catalog)
	if len(p.Fallback) > 0 {
		kv("fallback", fallbackLabel(p.Fallback))
	}
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
		if len(k) > len(headerPrefix) && strings.EqualFold(k[:len(headerPrefix)], headerPrefix) {
			// header.X-Foo=bar: the prefix is any casing, the name keeps the user's
			if err := p.SetHeader(k[len(headerPrefix):], v); err != nil {
				return err
			}
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
			icon, err := iconArg(v)
			if err != nil {
				return err
			}
			if err := provider.CheckIcon(icon); err != nil {
				return err
			}
			p.Icon = icon
		default:
			return fmt.Errorf("unknown field %q\n\n%s", k, providerUsage)
		}
	}
	return nil
}

// hasPairKey reports whether any k=v pair names key (case-insensitively).
func hasPairKey(pairs []string, key string) bool {
	for _, kv := range pairs {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(strings.TrimSpace(k), key) {
			return true
		}
	}
	return false
}

// iconArg turns what the user typed for an icon into an icon value: an
// existing image file is stored and becomes a file: reference; anything else
// is taken as a built-in name or an existing reference, and checked later.
func iconArg(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "file:") {
		return v, nil
	}
	info, err := os.Stat(v)
	if err != nil || info.IsDir() {
		return v, nil
	}
	if info.Size() > provider.MaxIcon {
		return "", fmt.Errorf("%s is %d KB; icons are limited to %d KB", v, info.Size()>>10, provider.MaxIcon>>10)
	}
	data, err := os.ReadFile(v)
	if err != nil {
		return "", err
	}
	return provider.StoreIcon(data)
}

// iconCmd: `magpie provider icon <id> <image file|built-in name|"">`
func iconCmd(rest []string) error {
	if len(rest) != 2 {
		return fmt.Errorf("magpie provider icon <id> <image file|built-in name|\"\">")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Preset != "" || p.Account != nil {
		return fmt.Errorf("%s has its own icon; only custom providers can set one", p.Name)
	}
	icon, err := iconArg(rest[1])
	if err != nil {
		return err
	}
	if err := provider.SetIcon(p.ID, icon); err != nil {
		return err
	}
	label := icon
	if label == "" {
		label = "generic"
	}
	fmt.Println(green.Render("✓"), p.Name, "icon", muted.Render(label))
	return nil
}

// fallbackCmd handles `magpie provider fallback <id> [provider/model …]`:
// with no list it shows the fallback models; with one it replaces them.
func fallbackCmd(rest []string) error {
	if len(rest) < 1 {
		return fmt.Errorf("magpie provider fallback <id> [provider/model …]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) == 1 {
		showFallback(*p)
		return nil
	}
	list := strings.FieldsFunc(strings.Join(rest[1:], " "), func(r rune) bool { return r == ',' || r == ' ' })
	if len(list) == 1 && (list[0] == "-" || list[0] == "none") {
		list = nil
	}
	if err := provider.SetFallback(p.ID, list); err != nil {
		return err
	}
	p2, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), p.Name, "fallback", muted.Render(fallbackLabel(p2.Fallback)))
	return nil
}

// fallbackLabel lists fallback models in order, or says there are none.
func fallbackLabel(fs []string) string {
	if len(fs) == 0 {
		return "none"
	}
	return strings.Join(fs, " → ")
}

// showFallback prints a provider's fallback list, one model per line.
func showFallback(p provider.Provider) {
	if len(p.Fallback) == 0 {
		fmt.Println(muted.Render(p.Name + " has no fallback · magpie provider fallback " + p.ID + " <provider/model …>"))
		return
	}
	fmt.Println(" ", bold.Render(p.Name), muted.Render("fallback, in order"))
	for i, f := range p.Fallback {
		fmt.Printf("  %d. %s\n", i+1, f)
	}
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
