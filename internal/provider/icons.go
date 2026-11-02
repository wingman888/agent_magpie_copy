package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest image StoreIcon accepts.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced image is kept, so a picture the
// user just chose survives until the provider that uses it is saved.
const iconGrace = time.Hour

var (
	iconNameRe    = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)
	builtinIconRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// IconDir is where chosen icon images live.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile maps a stored icon name (the part after "file:") to its path on
// disk. Anything that is not exactly a name StoreIcon hands out gives "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// IconPath resolves an icon value ("file:<name>") to a path; built-in
// names and invalid references give "".
func IconPath(value string) string {
	if name, ok := strings.CutPrefix(value, "file:"); ok {
		return IconFile(name)
	}
	return ""
}

func iconExt(data []byte) string {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 {
		return ".ico"
	}
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	s := strings.TrimLeft(strings.TrimPrefix(string(head), "\xef\xbb\xbf"), " \t\r\n")
	if (strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml") || strings.HasPrefix(s, "<!--") || strings.HasPrefix(s, "<!DOCTYPE")) &&
		strings.Contains(strings.ToLower(string(data)), "<svg") {
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image under the config directory and returns the icon
// value that refers to it. The same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is larger than %d KB", MaxIcon>>10)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
		now := time.Now()
		os.Chtimes(path, now, now) // chosen again: keep it from the cleanup
		return "file:" + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
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
	return "file:" + name, nil
}

// ValidIcon reports whether an icon value is acceptable: empty, a built-in
// name, or a reference to a stored image.
func ValidIcon(v string) bool {
	if v == "" {
		return true
	}
	if name, ok := strings.CutPrefix(v, "file:"); ok {
		return iconNameRe.MatchString(name)
	}
	return builtinIconRe.MatchString(v)
}

// pruneIcons deletes stored images no provider refers to, unless they are
// younger than the grace period.
func pruneIcons(providers []Provider) {
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		if !e.Type().IsRegular() || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(IconDir(), e.Name()))
		}
	}
}
