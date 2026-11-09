package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// keyCmd runs one key verb. A key is named by its fingerprint or by its name.
func keyCmd(verb string, rest []string) error {
	if verb == "add-key" {
		return addKeyCmd(rest)
	}
	need := 2
	if verb == "rename-key" || verb == "key-protocol" {
		need = 3
	}
	if len(rest) != need {
		return fmt.Errorf("%s", providerUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	ref, err := keyArg(p, rest[1])
	if err != nil {
		return err
	}
	switch verb {
	case "use-key":
		err = provider.UseKey(p.ID, ref)
	case "key-on":
		err = provider.SetKeyOn(p.ID, ref, true)
	case "key-off":
		err = provider.SetKeyOn(p.ID, ref, false)
	case "rm-key":
		err = provider.RemoveKey(p.ID, ref)
	case "rename-key":
		err = provider.RenameKey(p.ID, ref, rest[2])
	case "key-protocol":
		err = provider.SetKeyProtocol(p.ID, ref, provider.Protocol(rest[2]))
	}
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), p.Name, verb, muted.Render(ref))
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	return showProvider(*saved)
}

// addKeyCmd: `magpie provider add-key <id> <key> [name=…] [protocol=…]`.
func addKeyCmd(rest []string) error {
	if len(rest) < 2 {
		return fmt.Errorf("magpie provider add-key <id> <key> [name=…] [protocol=chat|responses|anthropic]")
	}
	name, proto := "", ""
	for _, kv := range rest[2:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q\n\n%s", kv, providerUsage)
		}
		switch strings.ToLower(k) {
		case "name":
			name = v
		case "protocol", "proto":
			proto = v
		default:
			return fmt.Errorf("unknown field %q for add-key (name, protocol)", k)
		}
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if err := provider.AddKey(p.ID, name, rest[1], provider.Protocol(proto)); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), p.Name, "key", muted.Render(provider.Mask(rest[1])), muted.Render(provider.KeyRef(rest[1])))
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	return showProvider(*saved)
}

// keyArg resolves a fingerprint or a key name to the fingerprint.
func keyArg(p *provider.Provider, arg string) (string, error) {
	for _, k := range p.KeyInfos() {
		if strings.EqualFold(k.Ref, arg) || (k.Name != "" && strings.EqualFold(k.Name, arg)) {
			return k.Ref, nil
		}
	}
	return "", fmt.Errorf("%s has no key %q — magpie provider %s lists them", p.Name, arg, p.ID)
}

// keyLine describes one key for `magpie provider <id>`.
func keyLine(k provider.KeyInfo) string {
	state := "enabled"
	switch {
	case k.Current:
		state = "current"
	case k.Off:
		state = "off"
	}
	name := k.Name
	if name == "" {
		name = "(unnamed)"
	}
	who := string(k.Protocol)
	if who == "" {
		who = "any protocol"
	}
	line := fmt.Sprintf("%s  %s  %s  %s", k.Ref, name, k.Masked, muted.Render(who))
	if k.Off {
		return faint.Render(line + "  " + state)
	}
	return line + "  " + muted.Render(state)
}
