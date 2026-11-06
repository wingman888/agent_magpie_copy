package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage: magpie import [-y] <link>
  e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
       magpie import 'magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&key=sk-…'`

// importCmd: `magpie import [-y] <link>`. It shows what the link would add and
// saves only after the user confirms; -y skips the question.
func importCmd(args []string) error {
	var link string
	yes := false
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link != "":
			return fmt.Errorf("one link at a time\n\n%s", importUsage)
		default:
			link = a
		}
	}
	if link == "" {
		return fmt.Errorf("%s", importUsage)
	}

	// Errors from ParseImport never quote the link, so the key stays out of them.
	p, err := provider.ParseImport(link)
	if err != nil {
		return fmt.Errorf("invalid import link: %w", err)
	}

	// An id that belongs to a signed-in agent (or a removed one, which All()
	// does not list) cannot be saved as a plain provider: provider.save hands
	// such an id to the account branch and drops the link's content. Refuse
	// before anything is written.
	if removed, ok := accountIDState(p.ID); ok {
		if removed {
			return fmt.Errorf("id belongs to a removed account; choose another id or run magpie provider rm/add")
		}
		return fmt.Errorf("%s is a signed-in agent, not a provider; use id=… to pick another id", p.ID)
	}

	current := providerByID(p.ID)

	if !yes && !stdinIsTerminal() {
		return errors.New("not an interactive terminal: pass -y to add the provider without asking")
	}

	showImport(p, current != nil)

	if !yes {
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println(muted.Render("not added"))
			return nil
		}
	}

	if err := provider.SaveImport(p); err != nil {
		return err
	}
	saved := providerByID(p.ID)
	if saved == nil {
		return fmt.Errorf("saved %s but could not read it back", p.ID)
	}
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key", saved.ID, "<key>")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ms, err := saved.Fetch(ctx); err == nil {
		fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
	}
	if n := len(saved.Exposed()); n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
	return nil
}

// showImport prints what the link will add. The key is never printed in full.
func showImport(p provider.Provider, replaces bool) {
	kv := func(k, v string) {
		fmt.Printf("  %s %s\n", muted.Render(pad(k, 10)), v)
	}
	fmt.Println(" ", bold.Render(p.Name)+muted.Render("  "+p.ID))
	var hosts []string
	seen := map[string]bool{}
	for _, proto := range provider.Protocols {
		if u := p.Base(proto); u != "" {
			if h := provider.HostOf(u); !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	kv("sends to", strings.Join(hosts, ", "))
	kv("key", importKeyLabel(p.Key))
	switch {
	case len(p.Models) == 0:
		kv("models", muted.Render("fetched from the vendor after adding"))
	case len(p.Models) > 12:
		kv("models", fmt.Sprintf("%d · %s … %d more", len(p.Models), strings.Join(p.Models[:12], ", "), len(p.Models)-12))
	default:
		kv("models", strings.Join(p.Models, ", "))
	}
	if p.Catalog != "" {
		kv("catalog", p.Catalog)
	}
	if p.Website != "" {
		kv("website", p.Website)
	}
	if replaces {
		fmt.Println(amber.Render("!"), "will replace the existing provider", p.ID)
	}
}

// importKeyLabel shows only the first few characters of a key, or that one was given.
func importKeyLabel(key string) string {
	switch n := utf8.RuneCountInString(key); {
	case n == 0:
		return muted.Render("none given · add it later with magpie provider key")
	case n <= 8:
		return "provided"
	default:
		return string([]rune(key)[:4]) + "… provided"
	}
}

// providerByID finds a provider by exact id, or nil.
func providerByID(id string) *provider.Provider {
	for _, p := range provider.All() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// accountIDState reports whether id belongs to a signed-in agent account, and
// if so whether the user removed it. Removed accounts are missing from All(),
// so they are looked up in Accounts() and Excluded() as well.
func accountIDState(id string) (removed, isAccount bool) {
	for _, e := range provider.Excluded() {
		if e.Provider == id {
			return true, true
		}
	}
	for _, a := range provider.Accounts() {
		if a.ID == id {
			return false, true
		}
	}
	return false, false
}

// stdinIsTerminal reports whether standard input is an interactive terminal.
// /dev/null is a character device too, so it is excluded explicitly.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}
