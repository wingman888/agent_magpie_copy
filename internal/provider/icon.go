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

// MaxIcon is the largest icon file magpie will store (1 MB). Anything
// strictly larger is refused.
const MaxIcon = 1 << 20

// iconGrace is how long a stored icon is kept even when no provider refers
// to it, so an icon stored during an edit is not pruned before it is saved.
const iconGrace = time.Hour

// iconExts maps the content type detected by sniffing to its file extension.
var iconExts = map[string]string{
	"png":  ".png",
	"jpg":  ".jpg",
	"gif":  ".gif",
	"webp": ".webp",
	"ico":  ".ico",
	"svg":  ".svg",
}

// iconsDir is the directory custom icons are kept in: an "icons" folder next
// to providers.json.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon reports the extension for the image in data, or "" if the
// content is not a supported image type.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return iconExts["png"]
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return iconExts["jpg"]
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return iconExts["gif"]
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return iconExts["webp"]
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return iconExts["ico"]
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return iconExts["svg"]
	}
	return ""
}

// StoreIcon saves an image under the icons directory and returns the
// reference to put in a provider's icon field ("file:<name>"). The name is
// the first 16 hex digits of the content's SHA-256 plus the type's extension,
// so identical images share one file.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon is empty")
	}
	if len(data) > MaxIcon {
		return "", errors.New("icon too large, use an image under 1 MB")
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("unsupported icon format; use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		// Already stored: keep one copy, but refresh its time so it counts
		// as recent for the grace period.
		if err := os.Chtimes(path, now, now); err != nil {
			return "", err
		}
		return "file:" + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// StoreIconFile is StoreIcon for an image on disk. The size is checked
// before the file is read, so a huge file is never loaded into memory.
func StoreIconFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", path)
	}
	if fi.Size() > MaxIcon {
		return "", errors.New("icon too large, use an image under 1 MB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Read at most one byte past the limit, in case the file grew after Stat.
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// validIconName reports whether name is exactly 16 lowercase hex digits
// followed by one of the supported extensions.
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

// IconFile returns the path of the stored icon a reference names. The name
// may carry the "file:" prefix. Anything that is not a name StoreIcon could
// have produced returns "".
func IconFile(name string) string {
	name = strings.TrimPrefix(name, "file:")
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// iconRefName returns the stored-icon file name an icon field refers to, if
// it refers to one. Built-in icon names and "" are not references.
func iconRefName(icon string) (string, bool) {
	if !strings.HasPrefix(icon, "file:") {
		return "", false
	}
	name := strings.TrimPrefix(icon, "file:")
	if !validIconName(name) {
		return "", false
	}
	return name, true
}

// pruneIcons removes stored icons that no provider in f refers to and that
// were last written more than an hour ago. Failures are ignored: pruning is
// housekeeping and must not fail a save.
func pruneIcons(f file) {
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		return
	}
	used := make(map[string]bool)
	for _, p := range f.Providers {
		if name, ok := iconRefName(p.Icon); ok {
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
