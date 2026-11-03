package main

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// fallbackCmd shows or sets a provider's fallback list, in priority order:
//
//	magpie provider fallback <id>                 show the list
//	magpie provider fallback <id> <provider/model>…   set it; "" clears it
func fallbackCmd(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("magpie provider fallback <id> [provider/model …]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if len(rest) == 1 {
		if len(p.Fallback) == 0 {
			fmt.Println(muted.Render(p.Name + " has no fallback · magpie provider fallback " + p.ID + " <provider/model> …"))
			return nil
		}
		for i, ref := range p.Fallback {
			fmt.Printf("  %d. %s\n", i+1, ref)
		}
		return nil
	}
	if err := provider.SetFallback(p.ID, rest[1:]); err != nil {
		return err
	}
	after, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	if len(after.Fallback) == 0 {
		fmt.Println(green.Render("✓"), p.Name, "fallback", muted.Render("cleared"))
		return nil
	}
	fmt.Println(green.Render("✓"), p.Name, "fallback", muted.Render(strings.Join(after.Fallback, " → ")))
	return nil
}
