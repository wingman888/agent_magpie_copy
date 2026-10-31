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

// isImportLink is true for the first argument a protocol handler passes.
func isImportLink(s string) bool {
	return len(s) >= 7 && strings.EqualFold(s[:7], "magpie:")
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// importCmd: `magpie import [-y] <link>`
func importCmd(args []string) error {
	return runImport(args, os.Stdin, os.Stdout, stdinIsTerminal)
}

// maskKey shows only the start of a key, never all of it.
func maskKey(k string) string {
	if len(k) < 12 {
		return "provided"
	}
	return k[:4] + "…"
}

func runImport(args []string, in io.Reader, out io.Writer, isTTY func() bool) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case link == "" && (isImportLink(a) || !strings.HasPrefix(a, "-")):
			link = a
		default:
			return errors.New(importUsage)
		}
	}
	if link == "" {
		return errors.New(importUsage)
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}

	// what is about to happen, with the key masked
	fmt.Fprintf(out, "  %s  %s\n", bold.Render(p.Name), muted.Render("("+p.ID+")"))
	for _, pr := range provider.Protocols {
		if b := p.Base(pr); b != "" {
			fmt.Fprintf(out, "  %-10s %s\n", pr, b)
		}
	}
	hosts := map[string]bool{}
	var hl []string
	for _, pr := range provider.Protocols {
		if b := p.Base(pr); b != "" && !hosts[provider.HostOf(b)] {
			hosts[provider.HostOf(b)] = true
			hl = append(hl, provider.HostOf(b))
		}
	}
	fmt.Fprintf(out, "  requests and the key will be sent to: %s\n", strings.Join(hl, ", "))
	if p.Key != "" {
		fmt.Fprintf(out, "  key        %s\n", maskKey(p.Key))
	} else {
		fmt.Fprintf(out, "  key        none in the link\n")
	}
	if len(p.Models) > 0 {
		shown := p.Models
		more := ""
		if len(shown) > 12 {
			more = fmt.Sprintf(" … %d more", len(shown)-12)
			shown = shown[:12]
		}
		fmt.Fprintf(out, "  models     %s%s\n", strings.Join(shown, ", "), more)
	}
	for _, e := range provider.All() {
		if e.ID == p.ID {
			fmt.Fprintf(out, "  %s will replace the existing provider %s\n", amber.Render("!"), p.ID)
			break
		}
	}

	if !yes {
		if !isTTY() {
			return errors.New("not a terminal: nothing was added; pass -y to add without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Fprintln(out, "not added")
			return nil
		}
	}

	if err := provider.SaveKeyless(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	if !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no key yet · magpie provider key", saved.ID, "<key>")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Fprintln(out, green.Render("✓"), len(ms), "models from", saved.Host())
	}
	if n := len(saved.Exposed()); n == 0 {
		fmt.Fprintln(out, amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Fprintf(out, "  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}
