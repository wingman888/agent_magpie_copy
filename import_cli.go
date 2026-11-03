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

const importUsage = "usage: magpie import [-y] <magpie://import?…>"

// handOffToApp is where a running desktop app would take a magpie: link so
// it can show its own confirmation. Nothing sets it yet, so every link goes
// through the CLI flow in importCmd. A GUI build that wants the hand-off sets
// this; returning handled=false falls back to the CLI.
var handOffToApp func(link string) (handled bool, err error)

// isMagpieLink is true for a first argument that is a magpie: link, as the
// system protocol handler passes it.
func isMagpieLink(s string) bool {
	return len(s) >= len("magpie:") && strings.EqualFold(s[:len("magpie:")], "magpie:")
}

// importLink handles `magpie <magpie:…link>` as a launch from the system.
func importLink(args []string) error {
	if handOffToApp != nil {
		if handled, err := handOffToApp(args[0]); handled {
			return err
		}
	}
	return importCmd(args)
}

// importCmd: `magpie import [-y] <link>`. It parses the link, shows what
// would be added, and saves only after the user confirms.
func importCmd(args []string) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case a == "-h" || a == "--help":
			fmt.Println(importUsage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("one link at a time\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return errors.New(importUsage)
	}

	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	existing := false
	for _, have := range provider.All() {
		if have.ID == p.ID {
			existing = true
			break
		}
	}

	if !yes && !isTerminal(os.Stdin) {
		return fmt.Errorf("not an interactive terminal: pass -y to add %s without asking", p.ID)
	}

	showImport(os.Stdout, p, existing)
	if !yes && !confirm(os.Stdin, os.Stdout) {
		fmt.Println(muted.Render("not added"))
		return nil
	}

	if err := provider.SaveImport(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	reportAdded(saved)
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// showImport prints what an import would add. It never prints a whole key.
func showImport(w io.Writer, p provider.Provider, existing bool) {
	fmt.Fprintln(w, "Import", bold.Render(p.Name), muted.Render("("+p.ID+")"))
	if p.Preset != "" {
		fmt.Fprintln(w, "  preset  ", p.Preset)
	}
	fmt.Fprintln(w, "  sends to", strings.Join(p.ImportHosts(), ", "), muted.Render("(requests and key)"))
	fmt.Fprintln(w, "  key     ", keyForDisplay(p.Key))
	if len(p.Models) > 0 {
		shown := p.Models
		more := ""
		if len(shown) > 10 {
			shown, more = shown[:10], fmt.Sprintf(" … %d more", len(p.Models)-10)
		}
		fmt.Fprintln(w, "  models  ", strings.Join(shown, ", ")+more)
	}
	if existing {
		fmt.Fprintln(w, amber.Render("!"), "will replace the existing provider", p.ID)
	}
}

// keyForDisplay shows only the first four characters of a long key.
func keyForDisplay(key string) string {
	if key == "" {
		return muted.Render("not provided")
	}
	r := []rune(key)
	if len(r) <= 12 {
		return muted.Render("provided")
	}
	return muted.Render(fmt.Sprintf("provided (%s…)", string(r[:4])))
}

// confirm asks `Add it? [y/N]` and reads one answer; only y or yes is a yes.
func confirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Add it? [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// isTerminal reports whether f is a character device, i.e. an interactive terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
