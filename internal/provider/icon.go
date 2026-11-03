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
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest icon file magpie accepts, in bytes (1 MB).
const MaxIcon = 1 << 20

// iconGrace is how long a stored icon is kept before it may be collected,
// so an icon stored while the user is still editing a provider survives.
const iconGrace = time.Hour

// nowFunc is the clock used for icon mtimes and collection; tests replace it.
var nowFunc = time.Now

// iconName matches the only file names magpie writes into the icons
// directory: 16 lowercase hex digits (a SHA-256 prefix) and a known extension.
var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir is the "icons" directory next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon identifies the image type from its content, never its name.
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
	case bytes.Contains(data, []byte("<svg")):
		return ".svg", nil
	}
	return "", errors.New("unsupported icon format: use PNG, JPEG, GIF, WebP, ICO or SVG")
}

// StoreIcon saves an image in the icons directory and returns the reference
// to write into a provider's icon field ("file:<name>"). Identical content
// yields the same reference and a single file.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", errors.New("icon too large (max 1 MB), use a smaller image")
	}
	ext, err := sniffIcon(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext

	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	// Stamp the file as freshly stored, so it stays within the grace period.
	now := nowFunc()
	if err := os.Chtimes(p, now, now); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// StoreIconFile reads an image from disk and stores it with StoreIcon.
func StoreIconFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an icon file", path)
	}
	if fi.Size() > MaxIcon {
		return "", errors.New("icon too large (max 1 MB), use a smaller image")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Read at most one byte past the limit, in case the file grew since Stat.
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// IconFile maps an icon reference (with or without the "file:" prefix) to
// the path of the stored image. It returns "" for anything that StoreIcon
// could not have produced, so no path outside the icons directory is reachable.
func IconFile(name string) string {
	name = strings.TrimPrefix(name, "file:")
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// gcIcons removes stored icons that no provider in f references and that
// are older than the grace period. Best effort: errors are ignored.
func gcIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	dir := iconDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := nowFunc().Add(-iconGrace)
	for _, e := range ents {
		if e.IsDir() || !iconName.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// storeAndCollect saves providers.json, then drops icons nothing refers to.
func storeAndCollect(f file) error {
	if err := store(f); err != nil {
		return err
	}
	gcIcons(f)
	return nil
}
