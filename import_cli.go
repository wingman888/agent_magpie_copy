package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = "usage: magpie import [-y] <magpie://import?…>"

// isMagpieLink is true for an argument that is a magpie: link, which is how
// the operating system hands one over when it is opened.
func isMagpieLink(s string) bool {
	return len(s) >= len("magpie:") && strings.EqualFold(s[:len("magpie:")], "magpie:")
}

// importCmd: `magpie import [-y] <link>`, or `magpie <magpie:…link>`.
// It shows what the link would add, asks, and only then saves.
func importCmd(args []string) error {
	yes := false
	var links []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		default:
			links = append(links, a)
		}
	}
	if len(links) != 1 {
		return errors.New(importUsage)
	}
	p, err := provider.ParseImport(links[0])
	if err != nil {
		return err
	}
	for _, a := range provider.Accounts() {
		if a.ID == p.ID {
			return fmt.Errorf("%s is a signed-in account and cannot be replaced by an import", a.ID)
		}
	}

	existing, err := provider.Find(p.ID)
	replacing := err == nil
	if replacing && p.Key == "" && existing.Key != "" {
		// the link carries no key: keep the one already saved
		p.Key = existing.Key
	}

	showImport(p)
	if replacing {
		fmt.Println(amber.Render("!"), "will replace the existing provider", p.ID)
	}

	if !yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("not an interactive terminal; nothing added. Pass -y to add %s without asking", p.ID)
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
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	verb := "added"
	if replacing {
		verb = "replaced"
	}
	fmt.Println(green.Render("✓"), verb, saved.Name, muted.Render("("+saved.ID+")"))

	if saved.Ready() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ms, err := saved.Fetch(ctx); err == nil {
			fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
		}
	} else {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key", saved.ID, "<key>")
	}
	if n := len(saved.Exposed()); n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}

// showImport prints what the link would add. The key is never printed in
// full: only that one was provided, and its first few characters.
func showImport(p provider.Provider) {
	kv := func(k, v string) {
		fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	fmt.Println(" ", bold.Render(p.Name)+muted.Render("  "+p.ID))
	if p.Preset != "" {
		kv("preset", p.Preset)
	}
	var hosts []string
	for _, pr := range provider.Protocols {
		b := p.Base(pr)
		if b == "" {
			continue
		}
		kv(string(pr), b)
		if h := provider.HostOf(b); !containsString(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) > 0 {
		kv("sends to", strings.Join(hosts, ", "))
	}
	switch {
	case p.Key != "" && len(p.Key) <= 8:
		kv("key", "provided")
	case p.Key != "":
		kv("key", "provided ("+p.Key[:4]+"…)")
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("not in the link")+muted.Render("  add it later: magpie provider key "+p.ID+" …"))
	}
	if p.Catalog != "" {
		kv("catalog", p.Catalog)
	}
	if p.Website != "" {
		kv("website", p.Website)
	}
	if p.KeysURL != "" {
		kv("keys", p.KeysURL)
	}
	if len(p.Models) > 0 {
		kv("models", fmt.Sprintf("%d · %s", len(p.Models), strings.Join(firstN(p.Models, 6), ", ")))
	} else {
		kv("models", muted.Render("fetched from the vendor after adding"))
	}
}

// stdinIsTerminal reports whether standard input is a real terminal, not a
// pipe or /dev/null.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string(nil), xs[:n]...), fmt.Sprintf("… %d more", len(xs)-n))
}
