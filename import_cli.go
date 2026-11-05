package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage:
  magpie import [-y] <link>    show what a magpie://import link would add, then ask
                               -y, --yes   add without asking (needed when not in a terminal)
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'`

// importCmd: `magpie import [-y] <link>`. It also serves a magpie: link
// passed as the first argument by the system's protocol handler.
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n\n%s", a, importUsage)
		case link == "":
			link = a
		default:
			return fmt.Errorf("one link at a time\n\n%s", importUsage)
		}
	}
	if link == "" {
		return fmt.Errorf("magpie import [-y] <link>\n\n%s", importUsage)
	}

	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	old, replacing := existingProvider(p.ID)
	if replacing && old.Account != nil {
		return fmt.Errorf("%s is a signed-in agent, not a provider; choose another id with id=", p.ID)
	}
	if replacing && p.Key == "" && old.Key != "" {
		// the link carries no key: keep the one already saved rather than
		// dropping it with the rest of the old provider
		p.Key = old.Key
	}

	printImport(p, replacing)

	if !yes {
		if !stdinIsTerminal() {
			return errors.New("not an interactive terminal, so nothing was added; run again with -y to add without asking")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println(muted.Render("not added"))
			return nil
		}
	}

	if err := provider.SaveImported(p); err != nil {
		return err
	}
	saved, ok := existingProvider(p.ID)
	if !ok {
		return fmt.Errorf("saved %s but could not read it back", p.ID)
	}
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
	}
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key "+saved.ID+" <key>")
	}
	return nil
}

// printImport shows what the link would add. The key is never printed in
// full: only its first characters, or that one was provided.
func printImport(p provider.Provider, replacing bool) {
	fmt.Println(" ", bold.Render(p.Name)+muted.Render("  "+p.ID))
	kv := func(k, v string) {
		fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	kv("sends to", strings.Join(importHosts(p), ", "))
	kv("key", importKeyLabel(p.Key))
	if len(p.Models) > 0 {
		kv("models", strings.Join(p.Models, ", "))
	} else {
		kv("models", muted.Render("chosen by the vendor's list after adding"))
	}
	if p.Catalog != "" {
		kv("catalog", p.Catalog)
	}
	if replacing {
		fmt.Println(amber.Render("!"), "will replace the existing provider", p.ID)
	}
}

// importHosts lists the distinct hosts the provider's requests go to.
func importHosts(p provider.Provider) []string {
	var out []string
	for _, proto := range provider.Protocols {
		if u := p.Base(proto); u != "" {
			if h := provider.HostOf(u); !containsString(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// importKeyLabel masks a key for display: its first three characters, or
// just "provided" for a short one.
func importKeyLabel(k string) string {
	switch {
	case k == "":
		return muted.Render("not in the link; add it later with magpie provider key")
	case utf8.RuneCountInString(k) < 8:
		return "provided"
	default:
		return string([]rune(k)[:3]) + "…"
	}
}

// existingProvider finds a provider by exact id, without matching names.
func existingProvider(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// stdinIsTerminal reports whether a person can answer a prompt.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
