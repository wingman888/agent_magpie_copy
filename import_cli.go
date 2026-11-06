package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const importUsage = "usage: magpie import [-y] <链接>   e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'"

// maxConfirmLine bounds the answer read at the prompt; anything longer is
// refused rather than read to the end.
const maxConfirmLine = 256

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

// isTerminal reports whether f is an interactive terminal. A character device
// is not enough: /dev/null is one too. Nothing is saved unless the user answers
// the prompt, so a non-terminal stdin refuses.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// readConfirmLine reads one line of at most maxConfirmLine bytes. A longer line
// is an error, so the answer is never read to the end of an unbounded input.
func readConfirmLine(r *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return string(buf), nil
			}
			if err == io.EOF {
				return "", nil
			}
			return "", err
		}
		if b == '\n' {
			return string(buf), nil
		}
		if len(buf) >= maxConfirmLine {
			return "", errors.New("answer too long")
		}
		buf = append(buf, b)
	}
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
		line, err := readConfirmLine(bufio.NewReader(in))
		if err != nil {
			fmt.Fprintln(out)
			return fmt.Errorf("nothing added: %v", err)
		}
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			fmt.Fprintln(out)
			return errors.New("nothing added")
		}
	}

	// Startup migrations write to disk, so they run only now that the user
	// has agreed; the link path itself has written nothing before this point.
	settings.Migrate()
	agent.RenameLegacy()
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
