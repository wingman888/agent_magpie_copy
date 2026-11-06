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

// MaxIcon is the largest picture a custom provider's icon may be.
const MaxIcon = 1 << 20

// iconName is what a stored icon file is called: the first 8 bytes of the
// content's SHA-256 as 16 lowercase hex digits, then the extension the
// content was recognised as. Nothing else is ever read from icons/.
var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconsDir is the icons folder, next to providers.json.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile turns the name of a stored icon ("<hex>.<ext>", the part after
// "file:") into its path on disk. Any other name, including one with a path
// separator or "..", gives "".
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// BuiltinIcon reports whether name is one of the logos magpie ships.
func BuiltinIcon(name string) bool {
	for _, pr := range presets {
		if pr.Icon == name {
			return true
		}
	}
	return false
}

// StoreIcon saves a picture and returns the icon value that refers to it,
// "file:<name>". The same picture is kept once: its name comes from its
// content, not from where it was read.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d bytes; the limit is %d", len(data), MaxIcon)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("the icon is not an image: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// already stored; refresh its time so a tidy-up does not take it
		// while the user is still deciding to keep it
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return "file:" + name, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// sniffIcon names the extension a picture's content has, or "" when the
// content is not one of the formats magpie accepts.
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
	case looksLikeSVG(b):
		return ".svg"
	}
	return ""
}

// looksLikeSVG is true for an XML document whose root is an svg element.
func looksLikeSVG(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	b = bytes.TrimLeft(b, " \t\r\n")
	if !bytes.HasPrefix(b, []byte("<")) {
		return false
	}
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	return strings.Contains(strings.ToLower(string(head)), "<svg")
}

// saveAndTidy writes providers.json, then removes icons nothing refers to.
func saveAndTidy(f file) error {
	if err := store(f); err != nil {
		return err
	}
	tidyIcons(f)
	return nil
}

// tidyIcons deletes stored icons that no provider refers to and that are
// over an hour old. An icon picked a moment ago, not yet saved to a provider,
// is spared by that hour.
func tidyIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || used[name] || !iconName.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(iconsDir(), name))
	}
}
