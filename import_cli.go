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

const importUsage = "usage: magpie import [-y] <link>   e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'"

// importCmd: `magpie import [-y] <link>`, or a magpie: link given as the
// first argument (what the OS does when a user opens one). args are the
// arguments after the verb.
func importCmd(args []string) error {
	return runImport(args, os.Stdin, os.Stdout, isTerminal(os.Stdin))
}

// isImportLink reports whether a command-line argument is a magpie: link.
func isImportLink(arg string) bool {
	scheme, _, ok := strings.Cut(arg, ":")
	return ok && strings.EqualFold(scheme, provider.Scheme)
}

// isTerminal reports whether f is a character device, i.e. an interactive
// terminal. Nothing is saved unless the user answers the prompt, so a
// non-terminal stdin (a pipe, /dev/null) simply refuses.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runImport parses the link, shows what it would add and saves it only after
// the user agrees (or -y is given). It takes its streams as parameters so the
// confirmation logic can be tested.
func runImport(args []string, in io.Reader, out io.Writer, interactive bool) error {
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

	p, err := provider.ParseImport(links[0])
	if err != nil {
		return fmt.Errorf("cannot import: %v", err)
	}
	printImportSummary(out, p)

	for _, existing := range provider.All() {
		if existing.ID == p.ID {
			fmt.Fprintln(out, amber.Render("!"), "This will replace the existing provider", p.ID)
			break
		}
	}

	if !yes {
		if !interactive {
			return errors.New("nothing added: standard input is not a terminal; run again with -y to add without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			fmt.Fprintln(out)
			return errors.New("nothing added")
		}
	}

	saved, err := saveProviderWith(p, provider.SaveImported)
	if err != nil {
		return err
	}
	if p.Key == "" && !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no API key yet · magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// printImportSummary shows the provider a link would add, including every
// host that will receive the user's prompts and, if present, the API key.
func printImportSummary(w io.Writer, p provider.Provider) {
	fmt.Fprintln(w, bold.Render("Import:"), p.Name, muted.Render("("+p.ID+")"))
	if p.Preset != "" {
		fmt.Fprintln(w, muted.Render("  from preset "+p.Preset))
	}
	var hosts []string
	seen := map[string]bool{}
	for _, pr := range provider.Protocols {
		base := p.Base(pr)
		if base == "" {
			continue
		}
		host := provider.HostOf(base)
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
		fmt.Fprintf(w, "  %s %s\n", muted.Render(fmt.Sprintf("%-10s", string(pr))), base)
	}
	if len(p.Models) > 0 {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(fmt.Sprintf("%-10s", "models")), strings.Join(p.Models, ", "))
	} else {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(fmt.Sprintf("%-10s", "models")), muted.Render("fetched from the vendor after adding"))
	}
	if p.Key != "" {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(fmt.Sprintf("%-10s", "key")), provider.Mask(p.Key))
		fmt.Fprintf(w, "  Your prompts and API key will be sent to: %s\n", strings.Join(hosts, ", "))
	} else {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(fmt.Sprintf("%-10s", "key")), amber.Render("none in the link"))
		fmt.Fprintf(w, "  Your prompts will be sent to: %s\n", strings.Join(hosts, ", "))
		fmt.Fprintln(w, muted.Render("  You will add the key afterwards with magpie provider key <id> <key>."))
	}
}
