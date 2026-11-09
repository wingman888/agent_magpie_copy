package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const importUsage = `usage:
  magpie import [-y] <link>    e.g. magpie import 'magpie://import?preset=deepseek&key=sk-…'
                               shows what the link adds; asks before saving (-y skips the question)`

// importCmd: `magpie import [-y] <link>`
func importCmd(args []string) error {
	yes := false
	link := ""
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, importUsage)
		case link == "":
			link = a
		default:
			return fmt.Errorf("magpie import takes one link\n\n%s", importUsage)
		}
	}
	if link == "" {
		return fmt.Errorf("%s", importUsage)
	}
	return importLink(link, yes)
}

// handoffLink takes a magpie: link the system passed on a click. Without a
// terminal nothing can be confirmed, so nothing is saved: the link is checked,
// then the user is pointed at `magpie import` in a terminal. The link itself is
// not printed, since it carries the key.
func handoffLink(link string) error {
	if interactive() {
		return importLink(link, false)
	}
	if _, err := provider.ParseImport(link); err != nil {
		return err
	}
	return errors.New("link received, nothing added: run `magpie import <link>` in a terminal to review and add it")
}

// importLink adds the provider a link describes, after showing it and
// asking, unless yes is set. Nothing is written before the answer.
func importLink(link string, yes bool) error {
	p, err := provider.ParseImport(link)
	if err != nil {
		return err
	}
	existing := false
	for _, x := range provider.All() {
		if x.ID != p.ID {
			continue
		}
		if x.Account != nil {
			return fmt.Errorf("%s is a signed-in agent, not a custom provider; pick another id", p.ID)
		}
		existing = true
	}

	fmt.Println(bold.Render(p.Name), muted.Render("("+p.ID+")"))
	fmt.Println(muted.Render("  requests go to"), strings.Join(importHosts(p), ", "))
	if len(p.Models) == 0 {
		fmt.Println(muted.Render("  models"), "the vendor's list, fetched after adding")
	} else {
		fmt.Println(muted.Render("  models"), strings.Join(p.Models, ", "))
	}
	fmt.Println(muted.Render("  key"), importKeyLine(p.Key))
	if existing {
		fmt.Println(amber.Render("!"), "will replace the existing provider", p.ID)
	}

	if !yes {
		if !interactive() {
			return errors.New("not added: no terminal to confirm on; run again with -y to add without asking")
		}
		fmt.Print("Add it? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Println(muted.Render("not added"))
			return nil
		}
	}

	save := provider.Save
	if p.Key == "" {
		save = provider.SaveDraft
	}
	if err := save(p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	if err := reportAdded(saved); err != nil {
		return err
	}
	if !saved.Ready() {
		fmt.Println(amber.Render("!"), "no key yet ·", "magpie provider key", saved.ID, "<key>")
	}
	return nil
}

// importHosts lists the hosts the provider's base URLs point at, once each.
func importHosts(p provider.Provider) []string {
	var out []string
	for _, pr := range provider.Protocols {
		if u := p.Base(pr); u != "" {
			h := provider.HostOf(u)
			if !containsString(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// importKeyLine shows a key only by its first few characters.
func importKeyLine(key string) string {
	r := []rune(key)
	switch {
	case key == "":
		return muted.Render("none · add it later with magpie provider key")
	case len(r) > 8:
		return "provided (" + string(r[:4]) + "…)"
	default:
		return "provided"
	}
}

// interactive reports whether stdin is a terminal. Pipes and files are not;
// /dev/null is a character device too, so it is ruled out by name.
func interactive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
