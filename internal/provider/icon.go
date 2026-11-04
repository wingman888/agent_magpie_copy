package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxIcon is the largest icon file magpie will store (1 MB).
const MaxIcon = 1 << 20

// iconRefPrefix marks an Icon value that names a stored image file.
const iconRefPrefix = "file:"

// iconGrace is how long a freshly stored, unreferenced icon survives pruning,
// so an icon stored during an edit is not deleted before the provider is saved.
const iconGrace = time.Hour

// iconExts maps the content type to the file extension it is stored under.
var iconExts = []string{".png", ".jpg", ".gif", ".webp", ".ico", ".svg"}

// iconsDir is the directory stored icons live in: "icons" next to providers.json.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon returns the extension for a supported image, or "" if the content
// is not one. Binary formats are recognised by their magic bytes; SVG is text
// that contains an <svg tag.
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
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image under the icons directory and returns the
// "file:<name>" reference to put in a provider's icon field. The file name is
// the first 16 hex digits of the content's SHA-256 plus an extension, so the
// same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", errors.New("icon too large (max 1 MB), please use a smaller image")
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("unsupported icon type (use PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext

	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored: refresh its mtime so pruning does not take it
		// while the provider that will reference it is still being saved.
		now := time.Now()
		os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconRefPrefix + name, nil
}

// StoreIconFile is StoreIcon for an image on disk.
func StoreIconFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxIcon {
		return "", errors.New("icon too large (max 1 MB), please use a smaller image")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// validIconName reports whether name is exactly 16 lowercase hex digits plus
// one of the supported extensions. Anything else (paths, "..", other
// extensions) is not a stored icon.
func validIconName(name string) bool {
	for _, ext := range iconExts {
		if !strings.HasSuffix(name, ext) {
			continue
		}
		stem := name[:len(name)-len(ext)]
		if len(stem) != 16 {
			return false
		}
		for _, c := range stem {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	return false
}

// IconFile returns the path of the stored icon named by name (the part after
// "file:"), or "" if name is not a valid stored icon name or the file is gone.
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	path := filepath.Join(iconsDir(), name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// pruneIcons deletes stored icons that no provider in f references and that
// were last modified more than iconGrace ago. Files whose names are not
// stored-icon names are left alone.
func pruneIcons(f file) {
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconRefPrefix); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || used[name] || !validIconName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(iconsDir(), name))
	}
}
