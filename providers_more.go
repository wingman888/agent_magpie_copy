package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// customOnly is the error for an icon or header on a preset or a signed-in
// account, where the vendor's own look and headers are fixed.
func customOnly(p provider.Provider) error {
	return fmt.Errorf("%s: 仅自定义供应商可设置图标与请求头（预设供应商与订阅账号不可设置）", p.Name)
}

// resolveIcon turns what the user gave as an icon into the value stored on a
// provider: "" (the generic icon), a built-in logo name, or the stored
// "file:…" reference of a picture file.
func resolveIcon(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	// a built-in name wins: a file in the current directory that happens to
	// share its name is not read in its place
	if provider.BuiltinIcon(v) {
		return v, nil
	}
	if st, err := os.Stat(v); err == nil && !st.IsDir() {
		if st.Size() > provider.MaxIcon {
			return "", fmt.Errorf("%s is %d bytes; an icon may be at most %d", v, st.Size(), provider.MaxIcon)
		}
		b, err := os.ReadFile(v)
		if err != nil {
			return "", err
		}
		return provider.StoreIcon(b)
	}
	if strings.ContainsAny(v, `/\`) || isImageExt(filepath.Ext(v)) {
		return "", fmt.Errorf("no such icon file: %s", v)
	}
	return "", fmt.Errorf("unknown icon %q: give a picture file (PNG, JPEG, GIF, WebP, ICO or SVG) or a built-in icon name", v)
}

func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svg":
		return true
	}
	return false
}

// validHeaderName is an HTTP token, the only shape a header name may take.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// headerPair reads a `header.Name=value` argument, keeping the name's case.
func headerPair(k string) (string, bool) {
	const prefix = "header."
	if len(k) > len(prefix) && strings.EqualFold(k[:len(prefix)], prefix) {
		return strings.TrimSpace(k[len(prefix):]), true
	}
	return "", false
}

// setHeader records one custom request header on p.
func setHeader(p *provider.Provider, name, value string) error {
	if !validHeaderName(name) {
		return fmt.Errorf("header name %q is not valid", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header %s has a line break in its value", name)
	}
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	p.Headers[name] = value
	return nil
}

// iconCmd: `magpie provider icon <id> <image file | built-in name | "">`
func iconCmd(rest []string) error {
	if len(rest) != 2 {
		return errors.New(`magpie provider icon <id> <image file | built-in icon name | "">`)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Preset != "" || p.Account != nil {
		return customOnly(*p)
	}
	icon, err := resolveIcon(rest[1])
	if err != nil {
		return err
	}
	p.Icon = icon
	if err := provider.Save(*p); err != nil {
		return err
	}
	if icon == "" {
		fmt.Println(green.Render("✓"), p.Name, muted.Render("uses the generic icon"))
	} else {
		fmt.Println(green.Render("✓"), p.Name, "icon", muted.Render(icon))
	}
	return nil
}

// fallbackCmd: `magpie provider fallback <id> [provider/model …]`. With no
// list it shows the current one; "-" or "" clears it.
func fallbackCmd(rest []string) error {
	if len(rest) < 1 {
		return errors.New("magpie provider fallback <id> [provider/model …]")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	list := rest[1:]
	if len(list) == 0 {
		fmt.Println(bold.Render(p.Name), muted.Render(p.ID))
		if len(p.Fallback) == 0 {
			fmt.Println(muted.Render("  no fallback · magpie provider fallback " + p.ID + " <provider/model>…"))
		}
		for i, f := range p.Fallback {
			fmt.Printf("  %d. %s\n", i+1, f)
		}
		return nil
	}
	if len(list) == 1 && (list[0] == "-" || list[0] == "none") {
		list = nil
	}
	p.Fallback = list
	if err := provider.Save(*p); err != nil {
		return err
	}
	saved, err := provider.Find(p.ID)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), saved.Name, "fallback", muted.Render(fallbackText(saved.Fallback)))
	return nil
}

func fallbackText(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, " → ")
}

// headerLines lists a custom provider's request headers, sorted by name.
func headerLines(p provider.Provider) []string {
	h := p.CustomHeaders()
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+": "+h[n])
	}
	return out
}
