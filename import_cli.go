package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y|--yes] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
       magpie import 'magpie://import?name=My%20Relay&chat=https://relay.example/v1&key=sk-…'
  the link is only shown first; nothing is added until you answer y`

// isImportLink reports whether a command-line argument is a magpie: link, which
// is how the operating system hands one over on a protocol launch.
func isImportLink(arg string) bool {
	n := len(provider.Scheme) + 1
	return len(arg) >= n && strings.EqualFold(arg[:n], provider.Scheme+":")
}

// isTerminal reports whether f is a character device, i.e. an interactive
// console. A redirected or piped stdin is not, and cannot answer a prompt.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// importCmd: `magpie import [-y|--yes] <link>`, or `magpie 'magpie:…'`, where
// args[0] is the link itself.
func importCmd(args []string) error {
	return importLink(args, os.Stdin, os.Stdout, isTerminal(os.Stdin))
}

// importLink does the whole import flow. in, out and interactive are injected
// so the confirmation can be tested without a terminal.
func importLink(args []string, in io.Reader, out io.Writer, interactive bool) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("one import link at a time\n\n%s", importUsage)
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
	// provider.Save (shared with `provider add`) refuses a remote provider with
	// no key, so say so before asking rather than after a yes.
	if !p.Ready() {
		return fmt.Errorf("nothing added: this link has no key and %s needs one; "+
			"add key=… to the link (keyless links work only for local servers and no-key presets)",
			p.Host())
	}

	// Compare ids exactly: provider.Find would also match by name.
	var existing *provider.Provider
	for _, e := range provider.All() {
		if e.ID == p.ID {
			ee := e
			existing = &ee
			break
		}
	}

	printImportSummary(out, p)
	if existing != nil {
		fmt.Fprintln(out, amber.Render("!"), "This will replace the existing provider "+p.ID+
			muted.Render(" ("+existing.Name+")"))
	}

	if !yes {
		if !interactive {
			return errors.New("nothing added: stdin is not a terminal, so magpie cannot ask. " +
				"Re-run with -y to add without asking")
		}
		fmt.Fprint(out, "Add it? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return errors.New("nothing added")
		}
	}

	if err := provider.Save(p); err != nil {
		return err
	}
	saved, err := savedProvider(p.ID)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Fprintln(out, green.Render("✓"), len(ms), "models from", saved.Host())
	}
	if n := len(saved.Exposed()); n == 0 {
		fmt.Fprintln(out, amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Fprintf(out, "  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	if !saved.Ready() {
		fmt.Fprintln(out, amber.Render("!"), "no API key yet · add one with",
			"magpie provider key "+saved.ID+" <key>")
	}
	return nil
}

// savedProvider finds the provider just saved, by exact id.
func savedProvider(id string) (*provider.Provider, error) {
	for _, e := range provider.All() {
		if e.ID == id {
			ee := e
			return &ee, nil
		}
	}
	return nil, fmt.Errorf("provider %q was not found after saving", id)
}

// printImportSummary shows what the link would add, and, for every endpoint,
// the host that will receive prompts and the key.
func printImportSummary(out io.Writer, p provider.Provider) {
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(out, "  %s %s\n", muted.Render(pad(k, 10)), v)
		}
	}
	fmt.Fprintln(out, bold.Render(p.Name)+muted.Render("  "+p.ID))
	kv("preset", p.Preset)
	for _, e := range []struct{ label, url string }{
		{"chat", p.Chat},
		{"responses", p.Responses},
		{"anthropic", p.Anthropic},
	} {
		if e.url != "" {
			kv(e.label, e.url+muted.Render("  → prompts and key sent to ")+bold.Render(provider.HostOf(e.url)))
		}
	}
	if p.Key != "" {
		kv("key", provider.Mask(p.Key)+muted.Render("  (sent to the hosts above)"))
	} else {
		kv("key", amber.Render("none")+muted.Render("  · add one after importing"))
	}
	if len(p.Models) > 0 {
		kv("models", strings.Join(p.Models, ", "))
	} else {
		kv("models", muted.Render("the vendor's list, fetched after adding"))
	}
	kv("catalog", p.Catalog)
	kv("website", p.Website)
	kv("keys", p.KeysURL)
}
