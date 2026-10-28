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

const importUsage = "magpie import [-y] 'magpie://import?…'"

// importIO is what `magpie import` talks to; tests swap it out.
var importIO = struct {
	in         io.Reader
	isTerminal func() bool
}{
	in: os.Stdin,
	isTerminal: func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	},
}

// importCmd: `magpie import [-y] <link>` — show what a link would add, ask,
// then save it the way `magpie provider add` does.
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case link == "":
			link = a
		default:
			return fmt.Errorf("too many arguments: %s", importUsage)
		}
	}
	if link == "" {
		return errors.New(importUsage)
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}

	fmt.Println(bold.Render("This link adds a provider:"))
	if err := showProvider(p); err != nil {
		return err
	}
	fmt.Println(muted.Render("  prompts and your API key will be sent to: " + strings.Join(importHosts(p), ", ")))
	if old, err := provider.Find(p.ID); err == nil && old.ID == p.ID {
		fmt.Println(amber.Render("!"), "replaces your", old.Name)
	}
	if p.Key == "" && !p.Ready() {
		fmt.Println(amber.Render("!"), "the link has no key · add one after:", "magpie provider key", p.ID, "<key>")
	}

	if !yes {
		if !importIO.isTerminal() {
			return errors.New("not a terminal: add -y to import without asking")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(importIO.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("nothing added")
		}
	}
	return saveProvider(p)
}

// importHosts lists the distinct hosts a provider's endpoints point at.
func importHosts(p provider.Provider) []string {
	var hosts []string
	for _, e := range []string{p.Chat, p.Responses, p.Anthropic} {
		if e == "" {
			continue
		}
		h := strings.TrimPrefix(strings.TrimPrefix(e, "https://"), "http://")
		h, _, _ = strings.Cut(h, "/")
		dup := false
		for _, x := range hosts {
			dup = dup || x == h
		}
		if !dup {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// saveProvider stores p and fetches the vendor's model list.
func saveProvider(p provider.Provider) error {
	if err := provider.Save(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		saved, err = provider.Find(p.Name)
	}
	if err != nil {
		return err
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
	return nil
}
