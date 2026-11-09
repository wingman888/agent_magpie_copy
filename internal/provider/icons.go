package provider

// Icons a user picks for a custom provider. An image is copied into the
// icons folder next to providers.json and referenced as "file:<name>", where
// <name> is the first 8 bytes of its SHA-256 (16 hex digits) plus an
// extension named by the image's content. The name is the only thing that
// ever reaches the disk, so a reference cannot read anything else.

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

// MaxIcon is the largest image magpie will store.
const MaxIcon = 1 << 20

// FilePrefix marks an icon value that names a stored image.
const FilePrefix = "file:"

// iconGrace is how long an unreferenced image survives: long enough for a
// picked image to be saved with its provider.
const iconGrace = time.Hour

var (
	iconNameRe    = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)
	builtinIconRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// iconDir is the icons folder beside providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// StoreIcon saves an image and returns its icon value, "file:<name>". The
// same image is stored once; storing it again refreshes its timestamp so it
// is not swept before its provider is saved.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is larger than %d KB", MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image: magpie takes PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconDir()
	path := filepath.Join(dir, name)
	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		_ = os.Chtimes(path, now, now)
		return FilePrefix + name, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return FilePrefix + name, nil
}

// IconFile is the disk path of a stored image, given its file name. Anything
// that is not exactly a name StoreIcon could have produced gives "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// sniffIcon names the extension of an image by its content, or returns "".
func sniffIcon(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return ".ico"
	case looksSVG(b):
		return ".svg"
	}
	return ""
}

// looksSVG is true for UTF-8 text whose first markup is an <svg> element.
func looksSVG(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return false
	}
	s := strings.TrimLeft(string(bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))), " \t\r\n")
	if !strings.HasPrefix(s, "<") || strings.HasPrefix(strings.ToLower(s), "<html") {
		return false
	}
	if len(s) > 4096 {
		s = s[:4096]
	}
	return strings.Contains(strings.ToLower(s), "<svg")
}

// IconArg turns what the user typed into an icon value. "" means the generic
// icon; a path to an existing file is stored; anything else must be the name
// of a built-in icon.
func IconArg(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if st, err := os.Stat(arg); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("%s is a folder, not an image", arg)
		}
		if st.Size() > MaxIcon {
			return "", fmt.Errorf("%s is larger than %d KB", arg, MaxIcon>>10)
		}
		data, err := os.ReadFile(arg)
		if err != nil {
			return "", err
		}
		return StoreIcon(data)
	}
	if !builtinIconRe.MatchString(arg) {
		return "", fmt.Errorf("%q is neither an image file nor a built-in icon name", arg)
	}
	return arg, nil
}

// SetIcon sets a custom provider's icon from what the user typed: an image
// file (stored), a built-in name, or "" for the generic one. Presets and
// signed-in accounts keep their own, and are refused before anything is stored.
func SetIcon(id, arg string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil || p.Preset != "" {
		return fmt.Errorf("仅自定义供应商可设置图标：%s", p.Name)
	}
	icon, err := IconArg(arg)
	if err != nil {
		return err
	}
	p.Icon = icon
	return Save(*p)
}

// pruneIcons removes stored images no provider refers to and that are older
// than the grace period. Only files with magpie's own names are touched.
func pruneIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if name, ok := strings.CutPrefix(p.Icon, FilePrefix); ok {
			used[name] = true
		}
	}
	dir := iconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || used[name] || IconFile(name) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
