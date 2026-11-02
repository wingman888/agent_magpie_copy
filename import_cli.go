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

const importUsage = `usage: magpie import [-y|--yes] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'

The link only describes a provider. magpie shows what it would add, and where
your prompts and key would be sent, and saves nothing until you answer y.
-y skips the question (needed when stdin is not a terminal).`

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// importCmd: `magpie import [-y] <link>`; also the entry for a magpie:// link
// passed as the program's first argument.
func importCmd(args []string) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(strings.ToLower(a), "magpie:"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link == "":
			link = a
		default:
			return fmt.Errorf("too many arguments\n\n%s", importUsage)
		}
	}
	if link == "" {
		return fmt.Errorf("missing link\n\n%s", importUsage)
	}
	return runImport(link, yes, stdinIsTerminal(), os.Stdin, provider.SaveImported)
}

// runImport parses, summarizes, asks, and only then saves.
func runImport(link string, yes, interactive bool, in io.Reader, save func(provider.Provider) error) error {
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	if !yes && !interactive {
		return errors.New("refusing to import without confirmation: stdin is not a terminal; review the link and rerun with -y")
	}
	printImportSummary(p)
	if !yes {
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("nothing added")
		}
	}
	if err := saveAndReport(p, save); err != nil {
		return err
	}
	if saved, err := provider.Find(p.ID); err == nil && !saved.Ready() {
		fmt.Println(amber.Render("!"), "no API key yet ·", "magpie provider key", saved.ID, "<key>")
	}
	return nil
}

func printImportSummary(p provider.Provider) {
	fmt.Println("This link will add a provider:")
	fmt.Println()
	fmt.Printf("  name   %s\n  id     %s\n", p.Name, p.ID)
	fmt.Println("  Your prompts and API key will be sent to:")
	for _, e := range []struct{ label, url string }{{"chat", p.Chat}, {"responses", p.Responses}, {"anthropic", p.Anthropic}} {
		if e.url != "" {
			fmt.Printf("    %-10s %s  (host: %s)\n", e.label, e.url, provider.HostOf(e.url))
		}
	}
	if p.Key != "" {
		fmt.Printf("  key    %s\n", provider.Mask(p.Key))
	} else {
		fmt.Println("  key    none in the link")
	}
	if len(p.Models) > 0 {
		fmt.Printf("  models %s\n", strings.Join(p.Models, ", "))
	}
	for _, e := range provider.All() {
		if e.ID == p.ID {
			fmt.Println()
			fmt.Println(amber.Render("!"), "This will replace the existing provider", p.ID, "("+e.Name+")")
			break
		}
	}
	fmt.Println()
}
