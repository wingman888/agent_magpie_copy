package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage:
  magpie import [-y] <link>    add a provider from a magpie://import link
                               -y/--yes adds it without asking
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'`

// importCmd: `magpie import [-y] <link>`. Also reached when the first argument
// is a magpie: link handed over by the system's URL handler.
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link == "":
			link = a
		default:
			return fmt.Errorf("magpie import takes one link\n\n%s", importUsage)
		}
	}
	if link == "" {
		return fmt.Errorf("%s", importUsage)
	}

	// Parsing is validation only; an invalid link fails here, before anything is written.
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	replacing := false
	for _, have := range provider.All() {
		if have.ID == p.ID {
			replacing = true
		}
	}

	if !yes && !stdinIsTerminal() {
		return fmt.Errorf("magpie import needs a terminal to confirm; pass -y to add without asking")
	}

	showImport(p, replacing)
	if !yes {
		fmt.Print("Add it? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !isYes(answer) {
			fmt.Println(muted.Render("not added"))
			return nil
		}
	}

	if err := saveProvider(p); err != nil {
		return err
	}
	if p.Key == "" && !p.Ready() {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key", p.ID, "<key>")
	}
	return nil
}

// showImport prints what a link would add. Keys are never printed in full.
func showImport(p provider.Provider, replacing bool) {
	fmt.Println(bold.Render(p.Name) + muted.Render("  "+p.ID))
	var hosts []string
	for _, pr := range provider.Protocols {
		if base := p.Base(pr); base != "" {
			if h := provider.HostOf(base); !contains(hosts, h) {
				hosts = append(hosts, h)
			}
			fmt.Printf("  %s %s\n", muted.Render(pad(string(pr), 10)), base)
		}
	}
	fmt.Printf("  %s %s\n", muted.Render(pad("sends to", 10)), strings.Join(hosts, ", "))
	fmt.Printf("  %s %s\n", muted.Render(pad("key", 10)), importKey(p.Key))
	if len(p.Models) == 0 {
		fmt.Printf("  %s %s\n", muted.Render(pad("models", 10)), "the vendor's list, fetched after adding")
	} else {
		fmt.Printf("  %s %d\n", muted.Render(pad("models", 10)), len(p.Models))
		for i, m := range p.Models {
			if i == 20 {
				fmt.Println(faint.Render(fmt.Sprintf("             … %d more", len(p.Models)-20)))
				break
			}
			fmt.Println("             " + m)
		}
	}
	if replacing {
		fmt.Println(amber.Render("!"), "will replace the existing provider", p.ID)
	}
}

// importKey shows only the first few characters of a key.
func importKey(k string) string {
	r := []rune(k)
	switch {
	case k == "":
		return muted.Render("not given")
	case len(r) <= 8:
		return muted.Render("provided")
	default:
		return muted.Render(string(r[:4]) + "…")
	}
}

// saveProvider adds or replaces a provider and fetches its model list, the
// same way `magpie provider add` does.
func saveProvider(p provider.Provider) error {
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

func isYes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

// stdinIsTerminal is true when stdin is a character device other than /dev/null.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
