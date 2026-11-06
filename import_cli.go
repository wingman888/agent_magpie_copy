package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
       magpie import 'magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-…'
  the link is shown first; nothing is added until you say so (-y skips the question)`

// importCmd: `magpie import [-y|--yes] <link>`. It also serves a magpie: link
// given as the first argument, which is how the system hands links over.
func importCmd(args []string) error {
	yes := false
	var links []string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		default:
			links = append(links, a)
		}
	}
	if len(links) != 1 {
		return errors.New(importUsage)
	}
	return importLink(links[0], yes, os.Stdin, os.Stdout, stdinIsTerminal())
}

// importLink shows what a link would add, asks unless yes is set, and saves.
// Nothing is written before the user agrees. interactive says whether the
// question can be asked; without it and without yes, the link is refused.
func importLink(link string, yes bool, in io.Reader, out io.Writer, interactive bool) error {
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	existing := false
	for _, x := range provider.All() {
		if x.ID == p.ID {
			existing = true
		}
	}

	showImport(out, p)
	if existing {
		fmt.Fprintf(out, "%s will replace the existing provider %s\n", amber.Render("!"), p.ID)
	}
	if !yes {
		if !interactive {
			return errors.New("not an interactive terminal, so nothing was added; pass -y to add without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		reply, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(reply)) {
		case "y", "yes":
		default:
			fmt.Fprintln(out, muted.Render("not added"))
			return nil
		}
	}

	if err := provider.SaveImported(p); err != nil {
		return err
	}
	saved, err := findProvider(p.ID)
	if err != nil {
		return err
	}
	verb := "added"
	if existing {
		verb = "replaced"
	}
	fmt.Fprintln(out, green.Render("✓"), verb, saved.Name, muted.Render("("+saved.ID+")"))
	afterAdd(saved)
	if !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no key yet · magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// showImport prints what an import will add. The key is never printed in full.
func showImport(out io.Writer, p provider.Provider) {
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(out, "  %s %s\n", muted.Render(pad(k, 9)), v)
		}
	}
	name := bold.Render(p.Name) + muted.Render("  "+p.ID)
	if p.Preset != "" {
		name += faint.Render("  preset " + p.Preset)
	}
	fmt.Fprintln(out, "Import:")
	line("name", name)
	line("requests", importHosts(p))
	line("key", importKey(p.Key))
	if len(p.Models) > 0 {
		line("models", strings.Join(p.Models, ", "))
	} else {
		line("models", muted.Render("fetched from the vendor after adding"))
	}
	line("catalog", p.Catalog)
	line("website", p.Website)
}

// importHosts names the hosts requests go to, and the protocol each serves.
func importHosts(p provider.Provider) string {
	var parts []string
	seen := map[string]int{}
	for _, pr := range p.Speaks() {
		h := provider.HostOf(p.Base(pr))
		if i, ok := seen[h]; ok {
			parts[i] += ", " + string(pr)
			continue
		}
		seen[h] = len(parts)
		parts = append(parts, h+" ("+string(pr))
	}
	for i := range parts {
		parts[i] += ")"
	}
	return strings.Join(parts, "; ")
}

// importKey shows the first few characters of a key, or only that one is given.
func importKey(key string) string {
	r := []rune(key)
	switch {
	case key == "":
		return amber.Render("none") + muted.Render(" · add it after with magpie provider key")
	case len(r) < 12:
		return "provided"
	default:
		return string(r[:4]) + "… (provided)"
	}
}

func findProvider(id string) (provider.Provider, error) {
	for _, p := range provider.All() {
		if p.ID == id {
			return p, nil
		}
	}
	return provider.Provider{}, fmt.Errorf("provider %q was not saved", id)
}

// stdinIsTerminal reports whether standard input is an interactive terminal.
// /dev/null is a character device too, so it is excluded explicitly.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}
