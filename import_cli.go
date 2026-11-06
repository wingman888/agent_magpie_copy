package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = "usage: magpie import [-y] <magpie://import?…>"

// importCmd: `magpie import [-y|--yes] <link>`. It is also what runs when
// magpie is started with a magpie: link as its first argument, which is how
// the OS hands a clicked link over.
func importCmd(args []string) error {
	return importLink(args, os.Stdin, os.Stdout, isTerminal(os.Stdin))
}

// importLink does the work of importCmd. Nothing is written before the user
// confirms (or -y is given); every failure returns before any write.
func importLink(args []string, in io.Reader, out io.Writer, interactive bool) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-") && a != "-":
			return fmt.Errorf("unknown flag %s\n\n%s", a, importUsage)
		case link == "":
			link = a
		default:
			return fmt.Errorf("one link at a time\n\n%s", importUsage)
		}
	}
	if link == "" {
		return fmt.Errorf("%s", importUsage)
	}
	// ParseImport errors never contain the link, so they are safe to print.
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	if !yes && !interactive {
		return errors.New("magpie import asks before adding, and there is no terminal to ask in; pass -y to add without asking")
	}

	fmt.Fprintln(out, bold.Render(p.Name), muted.Render("("+p.ID+")"))
	fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("sends to", 10)), strings.Join(importHosts(p), ", "))
	switch {
	case p.Key != "":
		fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("key", 10)), "provided")
	case p.Ready():
		fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("key", 10)), muted.Render("none needed"))
	default:
		fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("key", 10)), amber.Render("not in the link")+muted.Render("  add it with magpie provider key "+p.ID+" <key>"))
	}
	if len(p.Models) > 0 {
		shown := p.Models
		more := ""
		if len(shown) > 12 {
			more = fmt.Sprintf(" … %d more", len(shown)-12)
			shown = shown[:12]
		}
		fmt.Fprintf(out, "  %s %s%s\n", muted.Render(pad("models", 10)), strings.Join(shown, ", "), more)
	} else {
		fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("models", 10)), muted.Render("the host's list, fetched after adding"))
	}

	if cur, ok := existingProvider(p.ID); ok {
		fmt.Fprintln(out, amber.Render("!"), "will replace the existing provider", cur)
	}

	if !yes {
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Fprintln(out, "not added")
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
	fmt.Fprintln(out, green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Fprintln(out, green.Render("✓"), len(ms), "models from", saved.Host())
	}
	switch {
	case !saved.Ready():
		fmt.Fprintln(out, amber.Render("!"), "no key yet ·", "magpie provider key", saved.ID, "<key>")
	case len(saved.Exposed()) == 0:
		fmt.Fprintln(out, amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	default:
		fmt.Fprintf(out, "  %d models in the catalog · %s\n", len(saved.Exposed()), muted.Render("magpie models"))
	}
	return nil
}

// importHosts lists the hosts the provider's requests will go to, once each.
func importHosts(p provider.Provider) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, pr := range p.Speaks() {
		h := provider.HostOf(p.Base(pr))
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// existingProvider reports the display name of a provider with exactly this id.
func existingProvider(id string) (string, bool) {
	for _, x := range provider.All() {
		if x.ID == id {
			return x.Name, true
		}
	}
	return "", false
}

// isMagpieLink is true for arguments such as magpie://import?… handed over by the OS.
func isMagpieLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), "magpie:")
}

// isTerminal reports whether f is a character device, i.e. an interactive terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
