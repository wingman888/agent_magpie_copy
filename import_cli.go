package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y|--yes] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
  the link's provider is shown first; nothing is added until you answer y`

// isImportLink reports whether a command-line argument is a magpie:// link
// (any case) rather than a command name. The OS hands links to magpie as the
// first argument when they are opened.
func isImportLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), provider.Scheme+":")
}

// importCmd is `magpie import [-y] <link>`. It is also the path taken when
// the first argument is a magpie:// link, in which case args[0] is the link.
//
// The link is parsed without writing or fetching anything. The provider is
// saved only after the user answers y, or straight away with -y.
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("too many arguments\n\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return fmt.Errorf("%s", importUsage)
	}

	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	existing, err := importExisting(p.ID)
	if err != nil {
		return err
	}

	fmt.Print(importSummary(p))
	if existing != nil {
		fmt.Print(importReplaceWarning(p, *existing))
	}

	if !yes {
		if !stdinIsTerminal() {
			return errors.New("standard input is not a terminal, so the import cannot be confirmed; nothing added (use -y to add without asking)")
		}
		fmt.Print("Add it? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !confirmed(answer) {
			return errors.New("nothing added")
		}
	}

	if p.Key == "" {
		err = provider.SaveWithoutKey(p)
	} else {
		err = provider.Save(p)
	}
	if err != nil {
		return err
	}
	if err := reportAdded(p); err != nil {
		return err
	}
	if saved := providerByID(p.ID); saved != nil && !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", p.ID, "<key>")
	}
	return nil
}

// importExisting returns the stored provider that id would replace, or nil
// when the id is free. An agent sign-in is not a provider that can be
// replaced, so an id that names one is refused.
func importExisting(id string) (*provider.Provider, error) {
	for _, p := range provider.All() {
		if p.ID != id {
			continue
		}
		if p.Account != nil {
			return nil, fmt.Errorf("%q is the id of %s's own sign-in, not a provider; pick another id=…", id, p.Name)
		}
		return &p, nil
	}
	return nil, nil
}

// providerByID returns the provider with exactly this id, or nil.
func providerByID(id string) *provider.Provider {
	for _, p := range provider.All() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// importSummary lists what the link would add, and where the prompts and the
// key would be sent. It is plain text, with no styling, so it reads the same
// anywhere.
func importSummary(p provider.Provider) string {
	var b strings.Builder
	row := func(k, v string) { fmt.Fprintf(&b, "  %-9s %s\n", k, v) }

	b.WriteString("This link adds a provider:\n")
	row("name", p.Name)
	row("id", p.ID)
	if p.Preset != "" {
		row("preset", p.Preset)
	}
	switch {
	case p.Key != "":
		row("key", provider.Mask(p.Key))
	case p.Ready():
		row("key", "none needed")
	default:
		row("key", "none in the link; you add one later with magpie provider key "+p.ID+" <key>")
	}

	b.WriteString("  your prompts go to:\n")
	for _, ep := range []struct {
		label, base string
	}{{"chat", p.Chat}, {"responses", p.Responses}, {"anthropic", p.Anthropic}} {
		if ep.base == "" {
			continue
		}
		fmt.Fprintf(&b, "    %-10s %s  (%s)\n", ep.label, provider.HostOf(ep.base), ep.base)
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		b.WriteString("    (no endpoint)\n")
	}

	if len(p.Models) > 0 {
		row("models", strings.Join(p.Models, ", "))
	} else {
		row("models", "the vendor's list, fetched after adding")
	}
	return b.String()
}

// importReplaceWarning is printed after the summary when the link's id is
// already taken. It names the provider that will be replaced.
func importReplaceWarning(link, existing provider.Provider) string {
	var b strings.Builder
	fmt.Fprintf(&b, "! This will replace the existing provider %s (%s).\n", existing.ID, existing.Name)
	if link.Key == "" && existing.Key != "" {
		b.WriteString("  The link has no key, so the stored key of that provider is dropped.\n")
	}
	return b.String()
}

// confirmed reports whether an answer to "Add it? [y/N]" is a yes. Only y or
// yes, in any case, counts; an empty answer or anything else is a no.
func confirmed(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

// stdinIsTerminal reports whether standard input is a character device, that
// is, someone at a terminal who can answer the prompt.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
