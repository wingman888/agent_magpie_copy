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

// MaxIcon is the largest image a custom provider may use as its icon: 1 MB.
const MaxIcon = 1 << 20

// iconGrace is how long a stored image is kept before it may be cleaned up
// while no provider refers to it, so an edit in progress is not lost.
const iconGrace = time.Hour

// iconExts maps each stored image type to its file extension.
var iconExts = []string{".png", ".jpg", ".gif", ".webp", ".ico", ".svg"}

// iconsDir is the directory next to providers.json where images are kept.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon identifies the image type from its content, not its name.
func sniffIcon(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", nil
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif", nil
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp", nil
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return ".ico", nil
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg", nil
	}
	return "", errors.New("unsupported image type: use PNG, JPEG, GIF, WebP, ICO or SVG")
}

// StoreIcon saves an image under the icons directory and returns the
// reference to put in a provider's icon field ("file:<name>"). The same
// content always gets the same name, so storing it twice keeps one copy.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("image is %d bytes, larger than the 1 MB limit; use a smaller image", len(data))
	}
	ext, err := sniffIcon(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext

	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored: don't rewrite it, but count it as freshly stored.
		now := time.Now()
		os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// StoreIconFile reads an image from disk and stores it with StoreIcon.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Read one byte past the limit so StoreIcon can reject oversized files
	// without loading them whole.
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// validIconName reports whether name is exactly 16 lowercase hex digits
// followed by one of the stored extensions.
func validIconName(name string) bool {
	ext := filepath.Ext(name)
	if !contains(iconExts, ext) {
		return false
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

// IconFile returns the path on disk of the image named by the part after
// "file:" in an icon reference, or "" if the name is not one StoreIcon
// produces or the image is missing.
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	path := filepath.Join(iconsDir(), name)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return path
}

// iconRef returns the stored image name an icon field refers to, or "".
func iconRef(icon string) string {
	if name, ok := strings.CutPrefix(icon, "file:"); ok {
		return name
	}
	return ""
}

// storeAndPrune writes the providers file, then cleans up images that no
// provider uses any more. Cleanup failures are ignored: they only leave
// extra files behind.
func storeAndPrune(f file) error {
	if err := store(f); err != nil {
		return err
	}
	pruneIcons(f)
	return nil
}

// pruneIcons deletes stored images that no provider in f refers to and that
// were last modified more than iconGrace ago.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name := iconRef(p.Icon); name != "" {
			used[name] = true
		}
	}
	dir := iconsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !validIconName(name) || used[name] {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
