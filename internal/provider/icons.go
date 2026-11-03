package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxIcon is the largest image accepted as a custom provider icon (1 MB).
const MaxIcon = 1 << 20

// iconGrace is how long a stored image is kept even when no provider refers
// to it, so an edit in progress does not lose the image it just stored.
const iconGrace = time.Hour

// iconExts maps each accepted content type to its file extension.
var iconExts = []string{".png", ".jpg", ".gif", ".webp", ".ico", ".svg"}

// iconDir is the directory custom icons are kept in, next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon returns the extension for the image type in data, or "" when the
// content is not one of the accepted types. Binary formats are recognised by
// their magic bytes; SVG by an <svg tag in the text.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return ".ico"
	case strings.Contains(strings.ToLower(string(data)), "<svg"):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image under the config directory and returns the
// reference to put in a provider's icon field ("file:<name>"). The name is
// the first 16 hex digits of the content's SHA-256 plus the extension, so the
// same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes; the limit is %d bytes (1 MB) — choose a smaller image", len(data), MaxIcon)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("unsupported icon: want PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext

	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		// Already stored: refresh its mtime so it counts as just stored.
		if err := os.Chtimes(path, now, now); err != nil {
			return "", err
		}
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// StoreIconFile reads an image from disk and stores it with StoreIcon.
func StoreIconFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", path)
	}
	if info.Size() > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes; the limit is %d bytes (1 MB) — choose a smaller image", info.Size(), MaxIcon)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// validIconName reports whether name has the form StoreIcon produces: 16
// lowercase hex digits and one of the accepted extensions.
func validIconName(name string) bool {
	for _, ext := range iconExts {
		if !strings.HasSuffix(name, ext) {
			continue
		}
		base := strings.TrimSuffix(name, ext)
		if len(base) != 16 {
			return false
		}
		for _, c := range base {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	return false
}

// IconFile returns the path of a stored icon given the name after "file:".
// Anything that is not a name StoreIcon could have produced yields "".
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// gcIcons deletes stored icons that no provider in f refers to and that are
// older than iconGrace. Errors are ignored: a leftover file is harmless.
func gcIcons(f file) {
	dir := iconDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || used[name] || !validIconName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
