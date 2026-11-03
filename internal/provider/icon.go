package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest image a custom provider may use as its icon.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced icon survives before a Save cleans
// it up. It covers the window between picking an image and saving the
// provider that uses it.
const iconGrace = time.Hour

// iconFilePrefix marks an icon value that points at a stored image.
const iconFilePrefix = "file:"

// iconOnlyCustom is the error for setting an icon on a preset or a signed-in
// account, whose icons come from magpie and the agent.
const iconOnlyCustom = "仅自定义供应商可设置图标 (only custom providers can set an icon)"

// builtinIcons are the logos bundled with magpie (internal/gui/assets/icons).
var builtinIcons = map[string]bool{
	"ai302-color": true, "aihubmix-color": true, "anthropic": true, "claude-color": true,
	"claudecode-color": true, "codex-color": true, "crush": true, "cursor": true,
	"deepseek-color": true, "fireworks-color": true, "gemini-color": true, "geminicli-color": true,
	"githubcopilot": true, "googlecloud-color": true, "goose": true, "groq": true, "kimi": true,
	"lmstudio": true, "minimax-color": true, "mistral-color": true, "moonshot": true, "ollama": true,
	"openai": true, "opencode": true, "openrouter": true, "pi": true, "qwen-color": true,
	"siliconcloud-color": true, "together-color": true, "xai": true, "yylx": true, "zai": true,
	"zhipu-color": true,
}

// BuiltinIcon reports whether name is one of the logos bundled with magpie.
func BuiltinIcon(name string) bool { return builtinIcons[name] }

// iconNameRe is a stored icon's file name: 8 bytes of SHA-256 in hex, then
// the extension detected from the content.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// svgRe matches an SVG document: an optional prolog (BOM, XML declaration,
// comments, DOCTYPE) followed by the <svg> root element.
var svgRe = regexp.MustCompile(`(?is)^(?:\s|\x{FEFF}|<\?[^>]*\?>|<!--.*?-->|<!DOCTYPE[^>]*>)*<svg[\s>/]`)

// iconsDir is the icons folder inside magpie's config directory.
func iconsDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// sniffIcon names the extension of image data by its content, or "" when
// the data is not one of the supported formats.
func sniffIcon(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif"
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return ".ico"
	case svgRe.Match(b):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image in the icons folder and returns its icon value,
// "file:<name>". The name is derived from the content, so the same image is
// stored once; storing it again refreshes its modification time.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes; the limit is %d", len(data), MaxIcon)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return iconFilePrefix + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return iconFilePrefix + name, nil
}

// IconFile turns a stored icon's file name into its path on disk. Anything
// that is not exactly a name StoreIcon produces returns "", so an icon value
// can never reach a file outside the icons folder.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// ResolveIcon turns what the user typed for an icon into an icon value: a
// local image file is stored and becomes "file:<name>", a bundled logo name
// is kept, and "" restores the generic icon.
func ResolveIcon(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if name, ok := strings.CutPrefix(v, iconFilePrefix); ok {
		if IconFile(name) == "" {
			return "", fmt.Errorf("%q is not a stored icon", v)
		}
		if _, err := os.Stat(IconFile(name)); err != nil {
			return "", fmt.Errorf("%q is not a stored icon", v)
		}
		return v, nil
	}
	if fi, err := os.Stat(v); err == nil && fi.Mode().IsRegular() {
		if fi.Size() > MaxIcon {
			return "", fmt.Errorf("%s is %d bytes; the icon limit is %d", v, fi.Size(), MaxIcon)
		}
		data, err := os.ReadFile(v)
		if err != nil {
			return "", err
		}
		return StoreIcon(data)
	}
	if BuiltinIcon(v) {
		return v, nil
	}
	return "", fmt.Errorf("%q is neither an image file nor a built-in icon name", v)
}

// SetIcon sets (or with "" clears) the icon of a custom provider. The value
// comes from ResolveIcon. Presets and signed-in accounts refuse.
func SetIcon(id, icon string) error {
	icon = strings.TrimSpace(icon)
	if err := checkIconValue(icon); err != nil {
		return err
	}
	if name, ok := strings.CutPrefix(icon, iconFilePrefix); ok {
		if _, err := os.Stat(IconFile(name)); err != nil {
			return fmt.Errorf("%q is not a stored icon", icon)
		}
	}
	if _, ok := find(Accounts(), id); ok {
		return errors.New(iconOnlyCustom)
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != id {
			continue
		}
		if f.Providers[i].Preset != "" {
			return errors.New(iconOnlyCustom)
		}
		f.Providers[i].Icon = icon
		return storeAndPrune(f)
	}
	return fmt.Errorf("no provider %q", id)
}

// checkIconValue accepts "", a bundled logo name, or a stored icon reference.
func checkIconValue(icon string) error {
	if icon == "" || BuiltinIcon(icon) {
		return nil
	}
	name, ok := strings.CutPrefix(icon, iconFilePrefix)
	if !ok || IconFile(name) == "" {
		return fmt.Errorf("unknown icon %q", icon)
	}
	return nil
}

// storeAndPrune writes the providers file, then removes icons nothing
// references and that are older than the grace period.
func storeAndPrune(f file) error {
	if err := store(f); err != nil {
		return err
	}
	refs := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconFilePrefix); ok {
			refs[name] = true
		}
	}
	pruneIcons(refs, time.Now())
	return nil
}

// pruneIcons deletes stored icons that no provider references and that were
// last written before the grace period. Files that are not icon names are
// left alone.
func pruneIcons(refs map[string]bool, now time.Time) {
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		return
	}
	cutoff := now.Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !iconNameRe.MatchString(name) || refs[name] {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(iconsDir(), name))
	}
}
