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
	"unicode/utf8"
)

// Icons for custom vendors. The user picks an image; magpie keeps a copy in
// the icons folder next to providers.json and the vendor refers to it as
// "file:<name>". A name is the first 8 bytes of the image's SHA-256 (16 hex
// digits) plus an extension that the image's content decides, so the same
// picture is only stored once and a name can never point outside the folder.

// MaxIcon is the largest image magpie will keep.
const MaxIcon = 1 << 20

const iconPrefix = "file:"

// iconName is the only shape of file name magpie will read or write.
var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is the folder the stored icons live in.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// StoreIcon keeps an image and returns the icon value that refers to it,
// "file:<name>". Empty data, data over MaxIcon, and anything that is not a
// PNG, JPEG, GIF, WebP, ICO or SVG are refused.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is larger than %d KB", MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can show: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored: touch it, so a picture the user has just picked
		// is not swept as unused before the provider is saved.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// IconFile is the path of a stored icon from its file name. It returns "" for
// any name that is not one StoreIcon could have made.
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// IconPath resolves an icon value to the file it refers to. Built-in names and
// anything else that is not a valid "file:" reference give "".
func IconPath(icon string) string {
	name, ok := strings.CutPrefix(icon, iconPrefix)
	if !ok {
		return ""
	}
	return IconFile(name)
}

// sniffIcon names the extension of an image by its content, or "" when it is
// none of the formats magpie shows.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case isSVG(data):
		return ".svg"
	}
	return ""
}

// isSVG reports whether the text's root element is <svg>, after any XML
// declaration, comments and doctype.
func isSVG(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return false
	}
	s := strings.TrimPrefix(string(data), "\uFEFF")
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		var end string
		switch {
		case strings.HasPrefix(s, "<?"):
			end = "?>"
		case strings.HasPrefix(s, "<!--"):
			end = "-->"
		case len(s) >= 9 && strings.EqualFold(s[:9], "<!doctype"):
			end = ">"
		default:
			if len(s) < 5 || !strings.EqualFold(s[:4], "<svg") {
				return false
			}
			switch s[4] {
			case ' ', '\t', '\r', '\n', '>', '/':
				return true
			}
			return false
		}
		i := strings.Index(s, end)
		if i < 0 {
			return false
		}
		s = s[i+len(end):]
	}
}

// pruneIcons removes stored icons that no provider in f refers to and that
// are more than an hour old. Anything newer is kept, since it may be a picture
// the user has chosen but not saved yet. Only files whose names StoreIcon
// could have made are touched.
func pruneIcons(f file) {
	dir := IconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || used[name] || !iconName.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
