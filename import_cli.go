package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/yetone/magpie/internal/provider"
)

// importCmd: `magpie import [-y] <link>`. The link only describes a provider;
// nothing is saved until the user has seen it and agreed (or passed -y).
func importCmd(args []string) error {
	yes := false
	var link string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case link == "" && (provider.IsImportLink(a) || !strings.HasPrefix(a, "-")):
			link = a
		default:
			return errors.New("usage: magpie import [-y] <magpie://import?…>")
		}
	}
	if link == "" {
		return errors.New("usage: magpie import [-y] <magpie://import?…>")
	}
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	fmt.Println(bold.Render("Add this provider?"))
	for _, l := range provider.DescribeImport(p) {
		fmt.Println("  " + l)
	}
	if provider.Exists(p.ID) {
		fmt.Println(amber.Render("!"), "will replace the existing provider "+p.ID)
	}
	if !yes {
		if !interactive() {
			return errors.New("not a terminal: nothing was added; run again with -y to add it")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println("not added")
			return nil
		}
	}
	if err := provider.SaveDraft(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	afterAdd(saved)
	if p.Key == "" && !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key", saved.ID, "<key>")
	}
	return nil
}

func interactive() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// afterAdd reports a freshly saved provider and fetches its model list.
func afterAdd(saved *provider.Provider) {
	fmt.Println(green.Render("✓"), "added", saved.Name, muted.Render("("+saved.ID+")"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if saved.Ready() {
		if ms, err := saved.Fetch(ctx); err == nil {
			fmt.Println(green.Render("✓"), len(ms), "models from", saved.Host())
		}
	}
	n := len(saved.Exposed())
	if n == 0 {
		fmt.Println(amber.Render("!"), "no models exposed yet ·", "magpie provider models", saved.ID, "<ids…>")
	} else {
		fmt.Printf("  %d models in the catalog · %s\n", n, muted.Render("magpie models"))
	}
}
