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

const importUsage = `usage:
  magpie import [-y] <link>     add the provider a magpie://import link describes
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'`

// Seams for tests: whether a question can be answered, and where the answer
// is read from.
var (
	importTerminal     = stdinIsTerminal
	importIn io.Reader = os.Stdin
)

// isImportLink reports whether a command-line argument is a magpie: link,
// which the operating system passes when the user opens one.
func isImportLink(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), provider.Scheme+":")
}

// importCmd: `magpie import [-y|--yes] <link>`, and `magpie magpie://…`.
// It shows what the link would add, asks before saving anything, and saves
// only on a yes.
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
	replacing := false
	for _, old := range provider.All() {
		if old.ID == p.ID {
			replacing = true
		}
	}

	printImport(p)
	if replacing {
		fmt.Println(amber.Render("!"), "This will replace the existing provider "+p.ID)
	}
	if !yes {
		if !importTerminal() {
			return errors.New("nothing added: standard input is not a terminal, so magpie cannot ask; pass -y to add it without asking")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(importIn).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return errors.New("nothing added")
		}
	}

	saved, err := storeAndReport(p, provider.SaveAllowNoKey)
	if err != nil {
		return err
	}
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet · magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// printImport shows what an import would add, with the hosts that would
// receive the user's prompts and key written out.
func printImport(p provider.Provider) {
	kv := func(k, v string) {
		fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	fmt.Println(" ", bold.Render("Import "+p.Name)+muted.Render("  "+p.ID))
	kv("name", p.Name)
	kv("id", p.ID)
	if p.Preset != "" {
		kv("preset", p.Preset)
	}
	var hosts []string
	for _, e := range []struct{ label, base string }{
		{"chat", p.Chat}, {"responses", p.Responses}, {"anthropic", p.Anthropic},
	} {
		if e.base == "" {
			continue
		}
		kv(e.label, e.base)
		hosts = append(hosts, bold.Render(provider.HostOf(e.base))+muted.Render(" ("+e.label+")"))
	}
	kv("sends to", strings.Join(hosts, ", ")+muted.Render("  your prompts and key go here"))
	if p.Key != "" {
		kv("key", provider.Mask(p.Key))
	} else {
		kv("key", amber.Render("none in the link")+muted.Render(" · you add it after"))
	}
	if len(p.Models) > 0 {
		kv("models", strings.Join(p.Models, ", "))
	} else {
		kv("models", muted.Render("chosen from the vendor's list"))
	}
	if p.Catalog != "" {
		kv("catalog", p.Catalog)
	}
	if p.Website != "" {
		kv("website", p.Website)
	}
	if p.KeysURL != "" {
		kv("keys", p.KeysURL)
	}
}

// stdinIsTerminal reports whether standard input is an interactive terminal.
// /dev/null is a character device too, but nothing can answer through it.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// storeAndReport saves p with save, fetches its model list and prints the
// result. `magpie provider add` and `magpie import` share it.
func storeAndReport(p provider.Provider, save func(provider.Provider) error) (*provider.Provider, error) {
	if err := save(p); err != nil {
		return nil, err
	}
	var saved *provider.Provider
	for _, q := range provider.All() {
		if q.ID == p.ID {
			saved = &q
			break
		}
	}
	if saved == nil {
		found, err := provider.Find(p.ID)
		if err != nil {
			found, err = provider.Find(p.Name)
		}
		if err != nil {
			return nil, err
		}
		saved = found
	}
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
	}
	n := len(saved.Exposed())
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return saved, nil
}
