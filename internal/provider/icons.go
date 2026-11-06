package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxIcon is the largest image accepted as a provider icon: 1 MB.
const MaxIcon = 1 << 20

// iconPrefix marks an icon that is a stored image rather than a built-in name.
const iconPrefix = "file:"

// iconGrace is how long a stored image survives without a provider pointing
// at it, so an icon saved but not yet written into a provider is not lost.
const iconGrace = time.Hour

// iconExts maps a detected content type to its file extension.
var iconExts = map[string]string{
	"png":  ".png",
	"jpg":  ".jpg",
	"gif":  ".gif",
	"webp": ".webp",
	"ico":  ".ico",
	"svg":  ".svg",
}

// iconDir is the directory stored icons live in: "icons" next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon reports the content type of an image, by its bytes.
func sniffIcon(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", true
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp", true
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return "ico", true
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return "svg", true
	}
	return "", false
}

// StoreIcon saves an image under the icons directory and returns the
// reference to write into a provider's icon field ("file:<name>"). The name
// is the first 16 hex digits of the content's SHA-256 plus an extension for
// the type, so the same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d bytes; the limit is 1 MB, try a smaller image", len(data))
	}
	kind, ok := sniffIcon(data)
	if !ok {
		return "", errors.New("not a supported image; use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + iconExts[kind]
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil {
		// already stored: refresh its time so it counts as new again
		now := time.Now()
		os.Chtimes(target, now, now)
	} else if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile reads an image from disk and stores it with StoreIcon.
// Files over MaxIcon are refused before they are read in full.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", path)
	}
	if st.Size() > MaxIcon {
		return "", fmt.Errorf("%s is %d bytes; the limit is 1 MB, try a smaller image", path, st.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// validIconName reports whether name is exactly the form StoreIcon produces:
// 16 lowercase hex digits and one of the known extensions.
func validIconName(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot != 16 {
		return false
	}
	ext := name[dot:]
	known := false
	for _, e := range iconExts {
		if e == ext {
			known = true
			break
		}
	}
	if !known {
		return false
	}
	for _, c := range name[:dot] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IconFile returns the path of a stored icon, given the name after "file:"
// (a "file:" prefix is accepted too). Anything that is not a name StoreIcon
// could have produced gives "", so nothing outside the icons directory is
// reachable.
func IconFile(name string) string {
	name = strings.TrimPrefix(name, iconPrefix)
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// pruneIcons deletes stored images that no provider refers to and that are
// older than iconGrace. It is best effort: a file it cannot remove is kept.
func pruneIcons() {
	entries, err := os.ReadDir(iconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range load().Providers {
		if strings.HasPrefix(p.Icon, iconPrefix) {
			used[strings.TrimPrefix(p.Icon, iconPrefix)] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !validIconName(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(iconDir(), name))
	}
}
