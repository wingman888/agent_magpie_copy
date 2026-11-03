package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
       magpie import 'magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-…'`

// importCmd: `magpie import [-y] <link>`, or a magpie: link given as the first argument.
func importCmd(args []string) error {
	return importLink(args, os.Stdin, isTerminal(os.Stdin), os.Stdout)
}

// importLink does the whole import: parse, show what would be added, ask,
// then save. Nothing is written before the user says yes (or -y is given).
// Input, terminal-ness and output are parameters so tests can drive it.
func importLink(args []string, in io.Reader, interactive bool, out io.Writer) error {
	link, yes, err := parseImportArgs(args)
	if err != nil {
		return err
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		// the link carries the key, so only the parser's own message is shown
		return fmt.Errorf("link refused, nothing added: %w", err)
	}

	fmt.Fprintln(out, bold.Render(p.Name), muted.Render("("+p.ID+")"))
	fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("sends to", 10)), strings.Join(importHosts(p), ", "))
	fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("key", 10)), keyStatus(p.Key))
	fmt.Fprintf(out, "  %s %s\n", muted.Render(pad("models", 10)), modelsStatus(p))

	if existing, ok := findByID(p.ID); ok && existing.Account != nil {
		return fmt.Errorf("%s is a signed-in account and cannot be replaced; pick another id", p.ID)
	} else if ok {
		fmt.Fprintln(out, amber.Render("!"), "will replace the existing provider", p.ID)
	}

	if !yes {
		if !interactive {
			return errors.New("not an interactive terminal: pass -y to add without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Fprintln(out, muted.Render("not added"))
			return nil
		}
	}

	if err := provider.SaveImported(p); err != nil {
		return err
	}
	if err := reportAdded(out, p); err != nil {
		return err
	}
	if saved, ok := findByID(p.ID); ok && !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no key yet ·", "magpie provider key", p.ID, "<key>")
	}
	return nil
}

// parseImportArgs reads `[import] [-y|--yes] <link>` in any order.
func parseImportArgs(args []string) (link string, yes bool, err error) {
	if len(args) > 0 && strings.EqualFold(args[0], "import") {
		args = args[1:]
	}
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return "", false, fmt.Errorf("give one link at a time\n\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return "", false, fmt.Errorf("no link given\n\n%s", importUsage)
	}
	return link, yes, nil
}

// isMagpieLink reports whether an argument is a magpie: link (any case).
func isMagpieLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), "magpie:")
}

// importHosts lists the hosts the provider's requests (and key) will reach.
func importHosts(p provider.Provider) []string {
	var hosts []string
	for _, pr := range provider.Protocols {
		if b := p.Base(pr); b != "" {
			if h := provider.HostOf(b); !contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// keyStatus says whether a key came with the link, without showing it.
func keyStatus(key string) string {
	if key == "" {
		return muted.Render("none in the link")
	}
	if utf8.RuneCountInString(key) <= 8 {
		return "provided"
	}
	return "provided " + muted.Render(string([]rune(key)[:4])+"…")
}

func modelsStatus(p provider.Provider) string {
	if len(p.Models) == 0 {
		return muted.Render("fetched from the vendor after adding")
	}
	return strings.Join(p.Models, ", ")
}

// findByID looks a provider up by its exact id, accounts included.
func findByID(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// isTerminal reports whether f is a character device (a console), not a pipe or file.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
