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

// importCmd: `magpie import [-y|--yes] <link>`, and `magpie <magpie:…link>`
// when the OS hands a link to the program. The link is parsed and summarised
// first; nothing is saved unless the user agrees (or passes -y).
func importCmd(args []string) error {
	link, yes, err := parseImportArgs(args)
	if err != nil {
		return err
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	existing := providerByID(p.ID)
	if existing != nil && existing.Account != nil {
		return fmt.Errorf("%s is a signed-in agent account, not a provider; pick another id", p.ID)
	}
	if err := requireConsent(yes, stdinIsTerminal()); err != nil {
		return err
	}

	printImportSummary(os.Stdout, p, existing)
	if !yes {
		ok, err := confirm(bufio.NewReader(os.Stdin), os.Stdout)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("nothing added")
		}
	}

	if err := provider.SaveImported(p); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "added", p.Name, muted.Render("("+p.ID+")"))
	if p.Ready() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ms, err := p.Fetch(ctx); err == nil {
			fmt.Println(green.Render("✓"), len(ms), "models from", p.Host())
		}
	} else {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", p.ID, "<key>")
	}
	if n := len(p.Exposed()); n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", p.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}

// parseImportArgs reads `[-y|--yes] <link>` in either order.
func parseImportArgs(args []string) (link string, yes bool, err error) {
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown flag %q\n%s", a, importUsage)
		case link != "":
			return "", false, fmt.Errorf("expected one link\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return "", false, errors.New(importUsage)
	}
	return link, yes, nil
}

// requireConsent refuses to add a provider without asking when nobody can
// answer: no -y and standard input is not a terminal.
func requireConsent(yes, interactive bool) error {
	if yes || interactive {
		return nil
	}
	return errors.New("standard input is not a terminal, so magpie cannot ask; add -y to add without asking")
}

// stdinIsTerminal reports whether standard input is a character device.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// confirm asks "Add it? [y/N]" and reports whether the answer is yes.
// Anything other than y or yes, including an empty line or end of input, is no.
func confirm(in *bufio.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "Add it? [y/N] ")
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

// printImportSummary shows what the link would add, including every host that
// will receive the prompts and the key, and warns when it replaces a provider.
func printImportSummary(w io.Writer, p provider.Provider, existing *provider.Provider) {
	kv := func(k, v string) {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	fmt.Fprintln(w, bold.Render("Add provider"))
	kv("name", p.Name)
	kv("id", p.ID)
	if p.Preset != "" {
		kv("preset", p.Preset)
	}
	switch {
	case p.Key != "":
		kv("key", muted.Render(provider.Mask(p.Key)))
	case p.Ready():
		kv("key", muted.Render("none needed"))
	default:
		kv("key", amber.Render("none in this link")+muted.Render(" · add it later with magpie provider key "+p.ID+" <key>"))
	}
	fmt.Fprintln(w, "  "+muted.Render("prompts and the key go to:"))
	for _, proto := range provider.Protocols {
		if base := p.Base(proto); base != "" {
			fmt.Fprintf(w, "    %s %s %s\n", pad(string(proto), 10), bold.Render(provider.HostOf(base)), muted.Render("("+base+")"))
		}
	}
	if p.Website != "" {
		kv("website", p.Website)
	}
	if len(p.Models) > 0 {
		kv("models", strings.Join(p.Models, ", "))
	} else {
		kv("models", muted.Render("none listed; magpie keeps the vendor's own list"))
	}
	if existing != nil {
		fmt.Fprintln(w, amber.Render("!"), "This will replace the existing provider "+p.ID+" ("+existing.Name+")")
	}
}

// providerByID returns the stored provider with exactly this id, or nil.
func providerByID(id string) *provider.Provider {
	for _, p := range provider.All() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// isImportLink reports whether a command-line argument is a magpie: link,
// in any letter case, as the operating system passes it for the protocol.
func isImportLink(arg string) bool {
	prefix := provider.Scheme + ":"
	return len(arg) >= len(prefix) && strings.EqualFold(arg[:len(prefix)], prefix)
}
