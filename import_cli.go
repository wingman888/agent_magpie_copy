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

const importUsage = `usage: magpie import [-y|--yes] '<magpie:// link>'
  shows what the link would add and asks before saving it; -y skips the question`

// isImportLink reports whether a command-line argument is a magpie:// link,
// which is how the operating system hands one over when it is opened.
func isImportLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), provider.Scheme+":")
}

// importCmd: `magpie import [-y|--yes] <link>`. The protocol handler calls it
// with just the link, so opening a link behaves like typing it.
func importCmd(args []string) error {
	yes := false
	var links []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
			}
			links = append(links, a)
		}
	}
	if len(links) != 1 {
		return fmt.Errorf("%s", importUsage)
	}
	return runImport(links[0], yes, os.Stdin, os.Stdout, isTerminal(os.Stdin))
}

// runImport parses link, shows what it would add, and saves it only after the
// user confirms (or yes is set). interactive says whether the answer can come
// from a person; when it cannot and yes is false, nothing is saved.
func runImport(link string, yes bool, in io.Reader, out io.Writer, interactive bool) error {
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	for _, a := range provider.Accounts() {
		if a.ID == p.ID {
			return fmt.Errorf("%q is the id of a signed-in agent account; the link needs another id or name", p.ID)
		}
	}
	existing := false
	for _, e := range provider.All() {
		if e.ID == p.ID {
			existing = true
			break
		}
	}

	printImportSummary(out, p)
	if existing {
		fmt.Fprintln(out, amber.Render("!"), fmt.Sprintf("This will replace the existing provider %s", p.ID))
	}

	if !yes {
		if !interactive {
			return errors.New("not added: standard input is not a terminal, so there is no way to confirm; re-run with -y to add it without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return errors.New("nothing added")
		}
	}

	saved, err := saveProvider(p)
	if err != nil {
		return err
	}
	if !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no API key yet · add one with", "magpie provider key "+saved.ID+" <key>")
	}
	return nil
}

// printImportSummary lists what the link adds, including every host that will
// receive the prompts and, when there is one, the API key.
func printImportSummary(w io.Writer, p provider.Provider) {
	row := func(k, v string) {
		fmt.Fprintf(w, "  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	fmt.Fprintln(w, "This link adds a provider:")
	row("name", bold.Render(p.Name)+muted.Render("  id "+p.ID))
	if p.Preset != "" {
		row("preset", p.Preset)
	}
	for _, ep := range []struct{ label, base string }{
		{"chat", p.Chat}, {"responses", p.Responses}, {"anthropic", p.Anthropic},
	} {
		if ep.base != "" {
			row(ep.label, ep.base)
		}
	}

	var hosts []string
	seen := map[string]bool{}
	for _, pr := range provider.Protocols {
		if u := p.Base(pr); u != "" {
			if h := provider.HostOf(u); !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	if p.Key != "" {
		row("key", provider.Mask(p.Key))
		row("sent to", amber.Render("your prompts and your API key will go to ")+bold.Render(strings.Join(hosts, ", ")))
	} else {
		row("key", muted.Render("none in the link; you add it later"))
		row("sent to", "your prompts will go to "+bold.Render(strings.Join(hosts, ", ")))
	}

	switch {
	case len(p.Models) > 0:
		row("models", strings.Join(p.Models, ", "))
	case p.Catalog != "":
		row("models", "the vendor's list, named from models.dev ("+p.Catalog+")")
	default:
		row("models", "the vendor's own list, fetched after adding")
	}
	if p.Catalog != "" && len(p.Models) > 0 {
		row("catalog", p.Catalog)
	}
	if p.Website != "" {
		row("website", p.Website)
	}
	if p.KeysURL != "" {
		row("keys", p.KeysURL)
	}
}

// isTerminal reports whether f is a character device, i.e. a terminal a
// person can answer on. Redirected or piped input is not.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
