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

// MaxIcon is the largest image kept as a provider icon.
const MaxIcon = 1 << 20

// iconGrace is how long an icon nothing refers to yet is kept: long enough
// to pick it and save the provider that uses it.
const iconGrace = time.Hour

// storedIcon is the only shape of file name StoreIcon produces: 8 bytes of
// SHA-256 in hex, then the extension the content was recognised as.
var storedIcon = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// builtinIcon is a name of an icon magpie ships with, such as "openai".
var builtinIcon = regexp.MustCompile(`^[a-z0-9][a-z0-9+-]*$`)

// IconDir is where chosen images are kept: an icons folder beside providers.json.
// Without a home directory there is no config folder to sit beside, so the
// icons go under the temp directory instead of a relative path.
func IconDir() string {
	if _, err := os.UserHomeDir(); err != nil {
		return filepath.Join(os.TempDir(), "magpie", "icons")
	}
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile maps a stored icon's file name to its path. Anything other than a
// name StoreIcon could have produced (paths, "..", other extensions, "") gives "".
func IconFile(name string) string {
	if !storedIcon.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// StoreIcon keeps an image in the icons folder and returns the icon value
// that refers to it ("file:<name>"). The same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is %d bytes; icons are limited to 1 MB", len(data))
	}
	ext := sniffImage(data)
	if ext == "" {
		return "", errors.New("not a supported image: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	if err := os.MkdirAll(IconDir(), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(IconDir(), name)
	if _, err := os.Stat(path); err == nil {
		// already stored: touch it so it counts as freshly chosen
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// IconValue turns what the user typed for an icon into the stored value: an
// image file is kept under icons/, a built-in name or a file: reference is
// taken as it is, and "" clears the icon.
func IconValue(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if name, ok := strings.CutPrefix(arg, "file:"); ok {
		if IconFile(name) == "" {
			return "", fmt.Errorf("%q is not a stored icon", arg)
		}
		return arg, nil
	}
	if st, err := os.Stat(arg); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("%s is a folder, not an image", arg)
		}
		if st.Size() > MaxIcon {
			return "", fmt.Errorf("%s is %d bytes; icons are limited to 1 MB", arg, st.Size())
		}
		data, err := os.ReadFile(arg)
		if err != nil {
			return "", err
		}
		return StoreIcon(data)
	}
	if builtinIcon.MatchString(arg) {
		return arg, nil
	}
	return "", fmt.Errorf("%q is neither an image file nor a built-in icon name", arg)
}

// sniffImage recognises the supported formats by their content. It returns
// the file extension, or "" when the bytes are not one of them.
func sniffImage(b []byte) string {
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
	case isSVG(b):
		return ".svg"
	}
	return ""
}

// svgStart matches the start of an SVG document: an optional XML declaration,
// comments or doctype, then the <svg> root element.
var svgStart = regexp.MustCompile(`(?is)^\s*(?:<\?xml[^>]*>\s*|<!--.*?-->\s*|<!DOCTYPE[^>]*>\s*)*<svg[\s>/]`)

func isSVG(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	if len(b) > 4096 {
		b = b[:4096]
	}
	return svgStart.Match(b)
}

// pruneIcons deletes stored icons that no provider in f refers to and that
// were last touched over an hour ago. Only files with names StoreIcon makes
// are considered; anything else in the folder is left alone.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		if e.IsDir() || used[e.Name()] || IconFile(e.Name()) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(IconDir(), e.Name()))
	}
}
