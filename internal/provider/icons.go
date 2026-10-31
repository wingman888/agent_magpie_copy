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

// MaxIcon is the largest custom icon image magpie keeps.
const MaxIcon = 1 << 20

const iconPrefix = "file:"

var iconExts = map[string]bool{".png": true, ".jpg": true, ".gif": true, ".webp": true, ".ico": true, ".svg": true}

func iconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

func iconExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image under the config directory and returns the
// reference ("file:<name>") to put in a provider's Icon.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon image is too large (%d bytes, limit %d): pick a smaller image", len(data), MaxIcon)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("icon must be a PNG, JPEG, GIF, WebP, ICO or SVG image")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		now := time.Now()
		os.Chtimes(path, now, now)
		return iconPrefix + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile is StoreIcon for an image on disk.
func StoreIconFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", path)
	}
	if st.Size() > MaxIcon {
		return "", fmt.Errorf("icon image is too large (%d bytes, limit %d): pick a smaller image", st.Size(), MaxIcon)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(b)
}

func validIconName(name string) bool {
	if len(name) < 16 || !iconExts[name[16:]] {
		return false
	}
	for _, c := range name[:16] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IconFile maps the part of an icon reference after "file:" to the image's
// path on disk, or "" when the name is not one StoreIcon makes.
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// sweepIcons deletes stored images no provider references and that are
// older than an hour.
func sweepIcons(f file) {
	ents, err := os.ReadDir(iconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if strings.HasPrefix(p.Icon, iconPrefix) {
			used[strings.TrimPrefix(p.Icon, iconPrefix)] = true
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range ents {
		if e.IsDir() || !validIconName(e.Name()) || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(iconDir(), e.Name()))
		}
	}
}
