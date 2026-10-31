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
  link: a magpie://import?… link from a vendor or relay; nothing is added until you confirm
  -y:   skip the question (for scripts)`

// importEnv is what the import flow talks to; tests replace it.
type importEnv struct {
	in         io.Reader
	out        io.Writer
	isTerminal func() bool
	existing   func(id string) bool
	finish     func(p provider.Provider) error // after confirmation: save, fetch models
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func defaultImportEnv() importEnv {
	return importEnv{
		in: os.Stdin, out: os.Stdout, isTerminal: stdinIsTerminal,
		existing: func(id string) bool {
			for _, p := range provider.All() {
				if p.ID == id {
					return true
				}
			}
			return false
		},
		finish: func(p provider.Provider) error { return saveAndReport(p, provider.SaveImport) },
	}
}

// importCmd: `magpie import [-y] <link>`
func importCmd(args []string) error {
	return runImport(args[1:], defaultImportEnv())
}

func runImport(args []string, env importEnv) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case link == "" && (!strings.HasPrefix(a, "-") || strings.HasPrefix(strings.ToLower(a), "magpie:")):
			link = a
		default:
			return fmt.Errorf("unexpected argument %q\n\n%s", a, importUsage)
		}
	}
	if link == "" {
		return errors.New(importUsage)
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	out := env.out
	printImportSummary(out, p)
	replace := env.existing(p.ID)
	if replace {
		fmt.Fprintf(out, "\n%s This will replace the existing provider %s.\n", amber.Render("!"), p.ID)
	}
	fmt.Fprintln(out)
	if !yes {
		if !env.isTerminal() {
			return errors.New("refusing to add a provider without confirmation: stdin is not a terminal (use -y to confirm)")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(env.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("nothing added")
		}
	}
	if err := env.finish(p); err != nil {
		return err
	}
	if p.Key == "" {
		if saved, err := provider.Find(p.ID); err == nil && !saved.Ready() {
			fmt.Fprintf(out, "%s no key yet · magpie provider key %s <key>\n", amber.Render("!"), p.ID)
		}
	}
	return nil
}

func printImportSummary(w io.Writer, p provider.Provider) {
	fmt.Fprintf(w, "This link adds a provider:\n\n")
	fmt.Fprintf(w, "  name    %s\n  id      %s\n", p.Name, p.ID)
	if p.Preset != "" {
		fmt.Fprintf(w, "  preset  %s\n", p.Preset)
	}
	fmt.Fprintf(w, "\n  Your prompts and API key will be sent to these hosts:\n")
	for _, pr := range provider.Protocols {
		if u := p.Base(pr); u != "" {
			fmt.Fprintf(w, "    %-10s %s   (host: %s)\n", pr, u, provider.HostOf(u))
		}
	}
	if p.Key != "" {
		fmt.Fprintf(w, "\n  key     included (%s)\n", provider.Mask(p.Key))
	} else {
		fmt.Fprintf(w, "\n  key     none in the link\n")
	}
	if len(p.Models) > 0 {
		fmt.Fprintf(w, "  models  %s\n", strings.Join(p.Models, ", "))
	} else {
		fmt.Fprintf(w, "  models  the vendor's list / preset picks\n")
	}
}
