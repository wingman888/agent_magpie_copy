package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const keysUsage = `usage:
  magpie provider keys <id>                       list the keys; the current one is marked
  magpie provider keys <id> add <key> [name]      add a key; it is enabled and tried after the others
  magpie provider keys <id> use <ref|name>        make a key the current one (the old one stays enabled)
  magpie provider keys <id> on <ref|name>         enable a key
  magpie provider keys <id> off <ref|name>        disable a key; the last enabled key cannot be turned off
  magpie provider keys <id> rename <ref|name> <name>
  magpie provider keys <id> rm <ref|name>         remove a key; the last enabled key cannot be removed

  <ref> is the key's fingerprint, as the list shows it.`

// keysCmd: `magpie provider keys <id> [verb …]`
func keysCmd(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("%s", keysUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) == 1 {
		showKeys(*p)
		return nil
	}
	verb, args := rest[1], rest[2:]
	switch verb {
	case "add":
		if len(args) < 1 || len(args) > 2 {
			return fmt.Errorf("magpie provider keys %s add <key> [name]", p.ID)
		}
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		if err := provider.AddKey(p.ID, name, args[0], ""); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "added", muted.Render(provider.KeyRef(strings.TrimSpace(args[0]))))
	case "use", "on", "off", "rm", "remove", "delete":
		if len(args) != 1 {
			return fmt.Errorf("magpie provider keys %s %s <ref|name>", p.ID, verb)
		}
		ref, err := resolveKey(*p, args[0])
		if err != nil {
			return err
		}
		switch verb {
		case "use":
			err = provider.UseKey(p.ID, ref)
		case "on":
			err = provider.SetKeyOn(p.ID, ref, true)
		case "off":
			err = provider.SetKeyOn(p.ID, ref, false)
		default:
			err = provider.RemoveKey(p.ID, ref)
		}
		if err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), verb, muted.Render(ref))
	case "rename":
		if len(args) != 2 {
			return fmt.Errorf("magpie provider keys %s rename <ref|name> <name>", p.ID)
		}
		ref, err := resolveKey(*p, args[0])
		if err != nil {
			return err
		}
		if err := provider.RenameKey(p.ID, ref, args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "renamed", muted.Render(ref), "to", args[1])
	default:
		return fmt.Errorf("unknown keys command %q\n\n%s", verb, keysUsage)
	}
	p2, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	showKeys(*p2)
	return nil
}

// resolveKey turns a fingerprint or a key name into a fingerprint.
func resolveKey(p provider.Provider, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	var named []provider.KeyInfo
	for _, k := range p.KeyList() {
		if k.Ref == strings.ToLower(arg) {
			return k.Ref, nil
		}
		if k.Name != "" && strings.EqualFold(k.Name, arg) {
			named = append(named, k)
		}
	}
	switch len(named) {
	case 0:
		return "", fmt.Errorf("%s has no key %q; magpie provider keys %s lists them", p.Name, arg, p.ID)
	case 1:
		return named[0].Ref, nil
	default:
		return "", fmt.Errorf("more than one key is named %q; use its ref", arg)
	}
}

// showKeys prints a provider's keys with their fingerprints and state.
func showKeys(p provider.Provider) {
	keys := p.KeyList()
	if len(keys) == 0 {
		fmt.Println(muted.Render("no keys yet · magpie provider keys " + p.ID + " add <key>"))
		return
	}
	for _, k := range keys {
		state := muted.Render("● enabled")
		switch {
		case k.Current:
			state = green.Render("● current")
		case k.Off:
			state = faint.Render("○ off")
		}
		name := k.Name
		if name == "" {
			name = faint.Render("unnamed")
		}
		proto := string(k.Protocol)
		if proto == "" {
			proto = "any"
		}
		fmt.Printf("  %s  %s  %s  %s  %s\n", muted.Render(k.Ref), pad(state, 12), pad(k.Masked, 14), pad(name, 14), muted.Render(proto))
	}
}
