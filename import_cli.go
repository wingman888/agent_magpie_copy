package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
       -y, --yes   add without asking (required when standard input is not a terminal)`

// importStdinIsTerminal reports whether a person can answer the confirmation.
// It is a variable so that tests can simulate a script.
var importStdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// isImportLink reports whether a command-line argument is a magpie: link,
// which is how the operating system hands over a magpie:// URL.
func isImportLink(arg string) bool {
	return len(arg) >= len("magpie:") && strings.EqualFold(arg[:len("magpie:")], "magpie:")
}

// importCmd: `magpie import [-y] <link>`. args[0] is "import".
// Parsing writes nothing and makes no network calls; the provider is saved
// only after the user confirms (or passes -y).
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args[1:] {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("magpie import takes one link\n\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return fmt.Errorf("missing import link\n\n%s", importUsage)
	}

	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	if !yes && !importStdinIsTerminal() {
		return fmt.Errorf("standard input is not a terminal, so magpie cannot ask before adding a provider; review the link and run again with -y")
	}

	replacing := false
	for _, e := range provider.All() {
		if e.ID == p.ID {
			replacing = true
			break
		}
	}

	printImportSummary(p)
	if replacing {
		fmt.Println(amber.Render("!"), "This will replace the existing provider", p.ID)
	}
	if !yes {
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return fmt.Errorf("nothing added")
		}
	}

	if err := saveAndReport(p, provider.SaveWithoutKey); err != nil {
		return err
	}
	if !p.Ready() {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", p.ID, "<key>")
	}
	return nil
}

// printImportSummary shows what an import link would add. It names the hosts
// that will receive prompts and the key, and never prints the key itself.
func printImportSummary(p provider.Provider) {
	fmt.Println(bold.Render(p.Name), muted.Render(p.ID))
	if p.Preset != "" {
		fmt.Println(muted.Render("  from preset " + p.Preset))
	}
	fmt.Println("  Your prompts and key will be sent to:")
	for _, e := range []struct{ label, base string }{
		{"chat", p.Chat}, {"responses", p.Responses}, {"anthropic", p.Anthropic},
	} {
		if e.base != "" {
			fmt.Printf("    %-10s %s\n", e.label, provider.HostOf(e.base))
		}
	}
	if len(p.Models) > 0 {
		fmt.Printf("  models     %s\n", strings.Join(p.Models, ", "))
	} else {
		fmt.Printf("  models     %s\n", muted.Render("none listed · choose later with magpie provider models "+p.ID))
	}
	if p.Key != "" {
		fmt.Println("  key        included in the link (not shown)")
	} else {
		fmt.Println("  key        " + amber.Render("none in the link · add one after importing"))
	}
	if p.Website != "" {
		fmt.Println("  website    " + p.Website)
	}
	if p.KeysURL != "" {
		fmt.Println("  keys       " + p.KeysURL)
	}
}
