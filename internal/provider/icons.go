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

// MaxIcon is the largest image, in bytes, that can be used as an icon (1 MB).
const MaxIcon = 1 << 20

// iconPrefix marks a Provider.Icon that names an image magpie stored itself.
const iconPrefix = "file:"

// iconNameRe is the only shape of stored icon file name: the first 16 hex
// digits of the content's SHA-256, then the extension of its image type.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconsDir is the "icons" directory beside providers.json.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon names the image type of data by its content, or returns "" when
// it is not a supported format. Binary formats are recognized by their magic
// numbers; SVG by an <svg> tag in the text, whatever the file name says.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return "ico"
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return "svg"
	}
	return ""
}

// StoreIcon saves an image under the icons directory and returns the
// reference to write into a Provider's icon field ("file:<name>"). The same
// content always gets the same name, so it is stored only once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes, over the 1 MB limit; pick a smaller image", len(data))
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use as an icon (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored. Refresh its time so it counts as freshly stored and
		// survives until the provider that uses it is saved.
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			return "", err
		}
		return iconPrefix + name, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile reads an image from disk and stores it as StoreIcon does.
// The file's name plays no part in deciding its type.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Read one byte past the limit so StoreIcon can refuse oversize files
	// without loading them whole.
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// IconFile maps the name after "file:" in an icon reference to the image's
// path on disk. Only names StoreIcon produces are accepted; anything else,
// including path tricks, gives "". The file itself need not exist.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// pruneIcons deletes stored icons that no provider in f refers to and that
// were last written more than an hour before now. Only files named the way
// StoreIcon names them are candidates; anything else in the directory is left
// alone.
func pruneIcons(f file, now time.Time) error {
	dir := iconsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	cutoff := now.Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
	return nil
}
