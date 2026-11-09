package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const keyUsage = `usage:
  magpie provider key <id>                          list the keys, the current one first
  magpie provider key <id> <key>                    change the current key
  magpie provider key <id> add <key> [name=…] [protocol=chat|responses|anthropic]
  magpie provider key <id> use <ref>                make a key the current one
  magpie provider key <id> on|off <ref>             switch a key on or off
  magpie provider key <id> rm <ref>                 remove a key
  magpie provider key <id> rename <ref> <name>      name a key
  magpie provider key <id> protocol <ref> <chat|responses|anthropic|any>
                                                    limit a key to one protocol
  <ref> is the key's fingerprint, as the list shows it`

// keyVerbs are the words that make `provider key <id> …` a management command
// rather than a new current key.
var keyVerbs = map[string]bool{"add": true, "use": true, "on": true, "off": true,
	"rm": true, "remove": true, "delete": true, "rename": true, "protocol": true}

// keyCmd: `magpie provider key <id> …`
func keyCmd(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("%s", keyUsage)
	}
	id, args := rest[0], rest[1:]
	p, err := provider.Find(id)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return showProvider(*p)
	}
	verb := strings.ToLower(args[0])
	if len(args) == 1 && !keyVerbs[verb] {
		// the legacy form: the one key, replaced
		p.Key = args[0]
		if err := provider.Save(*p); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(p.Key)))
		return nil
	}
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("%s", keyUsage)
		}
		return nil
	}
	switch verb {
	case "add":
		if err := need(2); err != nil {
			return err
		}
		name, proto, err := keyOptions(args[2:])
		if err != nil {
			return err
		}
		if err := provider.AddKey(p.ID, name, args[1], proto); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "added a key to", p.Name, muted.Render(provider.KeyRef(args[1])))
	case "use":
		if err := need(2); err != nil {
			return err
		}
		if err := provider.UseKey(p.ID, args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), p.Name, "now uses key", muted.Render(args[1]))
	case "on", "off":
		if err := need(2); err != nil {
			return err
		}
		if err := provider.SetKeyOn(p.ID, args[1], verb == "on"); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "key", muted.Render(args[1]), "is", verb)
	case "rm", "remove", "delete":
		if err := need(2); err != nil {
			return err
		}
		if err := provider.RemoveKey(p.ID, args[1]); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed key", muted.Render(args[1]), "from", p.Name)
	case "rename":
		if err := need(3); err != nil {
			return err
		}
		name := strings.Join(args[2:], " ")
		if err := provider.RenameKey(p.ID, args[1], name); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "key", muted.Render(args[1]), "is named", name)
	case "protocol":
		if err := need(3); err != nil {
			return err
		}
		proto, err := parseKeyProtocol(args[2])
		if err != nil {
			return err
		}
		if err := provider.SetKeyProtocol(p.ID, args[1], proto); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "key", muted.Render(args[1]), "limited to", protoLabel(proto))
	}
	return nil
}

// keyOptions reads the name=… and protocol=… words after a key.
func keyOptions(words []string) (string, provider.Protocol, error) {
	var name string
	var proto provider.Protocol
	for _, w := range words {
		k, v, ok := strings.Cut(w, "=")
		if !ok {
			return "", "", fmt.Errorf("expected name=… or protocol=…, got %q\n\n%s", w, keyUsage)
		}
		switch strings.ToLower(k) {
		case "name":
			name = v
		case "protocol", "proto":
			p, err := parseKeyProtocol(v)
			if err != nil {
				return "", "", err
			}
			proto = p
		default:
			return "", "", fmt.Errorf("unknown key option %q\n\n%s", k, keyUsage)
		}
	}
	return name, proto, nil
}

// parseKeyProtocol reads a key protocol: chat, responses, anthropic, or any/"".
func parseKeyProtocol(s string) (provider.Protocol, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "any" || s == "none" {
		s = ""
	}
	p := provider.Protocol(s)
	if !provider.ValidKeyProtocol(p) {
		return "", fmt.Errorf("protocol %q is not one of chat, responses, anthropic, or any", s)
	}
	return p, nil
}

func protoLabel(p provider.Protocol) string {
	if p == "" {
		return "any protocol"
	}
	return string(p) + " only"
}

// printKeys lists a provider's keys, the current one first.
func printKeys(p provider.Provider) {
	for i, k := range p.KeyList() {
		label := ""
		if i == 0 {
			label = "keys"
		}
		mark := green.Render("●")
		if !k.On {
			mark = faint.Render("○")
		}
		name := k.Name
		if name == "" {
			name = "unnamed"
		}
		state := ""
		switch {
		case k.Current:
			state = "current"
		case !k.On:
			state = "off"
		}
		line := fmt.Sprintf("%s %s  %s  %s  %s", mark, k.Ref, muted.Render(k.Mask), name, muted.Render(protoLabel(k.Protocol)))
		if state != "" {
			line += "  " + muted.Render(state)
		}
		fmt.Printf("  %s %s\n", muted.Render(pad(label, 10)), line)
	}
}
