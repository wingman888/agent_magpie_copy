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
  the link is shown first; nothing is added until you answer y`

// importCmd: `magpie import [-y] <link>`, also reached when the first argument
// is a magpie: link handed over by the OS. The link is parsed (no disk, no
// network), summarised, and saved only after the user agrees.
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-") && !isImportLink(a):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("one import link at a time\n\n%s", importUsage)
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
	replacing := false
	for _, q := range provider.All() {
		if q.ID == p.ID {
			replacing = true
			break
		}
	}
	fmt.Print(importSummary(p, replacing))

	if !yes {
		if !stdinIsTerminal() {
			return errors.New("refusing to add a provider without asking: standard input is not a terminal; pass -y to add it anyway")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !confirmYes(line) {
			return errors.New("nothing added")
		}
	}

	save := provider.Save
	if p.Key == "" {
		save = provider.SaveWithoutKey
	}
	saved, err := saveAndReport(p, save)
	if err != nil {
		return err
	}
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// isImportLink reports whether an argument is a magpie: link (any case).
func isImportLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), provider.Scheme+":")
}

// stdinIsTerminal reports whether standard input is a character device, i.e.
// a person can answer the question.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// confirmYes accepts only y or yes, in any case. Empty input, or anything
// else, means no.
func confirmYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// importSummary describes what importing p will do: the endpoints, and the
// hosts that will receive prompts and the key. replacing adds the warning.
func importSummary(p provider.Provider, replacing bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  (id %s)", p.Name, p.ID)
	if p.Preset != "" {
		fmt.Fprintf(&b, "  preset %s", p.Preset)
	}
	b.WriteString("\n")

	var hosts []string
	seen := map[string]bool{}
	for _, proto := range p.Speaks() {
		u := p.Base(proto)
		h := provider.HostOf(u)
		fmt.Fprintf(&b, "  %-10s %s  → %s\n", proto, u, h)
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}

	if p.Key != "" {
		fmt.Fprintf(&b, "  key        %s\n", provider.Mask(p.Key))
	} else {
		b.WriteString("  key        no key (add one later with magpie provider key)\n")
	}

	if len(p.Models) > 0 {
		fmt.Fprintf(&b, "  models     %s\n", strings.Join(p.Models, ", "))
	} else {
		b.WriteString("  models     none listed; fetched from the provider when saved\n")
	}

	what := "Prompts"
	if p.Key != "" {
		what = "Prompts and key"
	}
	if len(hosts) == 0 {
		hosts = []string{"(no endpoint)"}
	}
	fmt.Fprintf(&b, "%s will be sent to: %s\n", what, strings.Join(hosts, ", "))

	if replacing {
		fmt.Fprintf(&b, "Warning: This will replace the existing provider %s\n", p.ID)
	}
	return b.String()
}
