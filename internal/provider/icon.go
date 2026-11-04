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

// MaxIcon is the largest icon magpie will store, in bytes (1 MB). Anything
// strictly bigger is refused.
const MaxIcon = 1 << 20

// iconPrefix marks an icon field that names a stored image rather than a
// built-in icon.
const iconPrefix = "file:"

// iconNameRe is the only shape a stored icon file name may take: the first
// 16 lowercase hex digits of the content's SHA-256, then the extension for
// its type. Nothing else (no separators, no dots beyond the extension) fits.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir is where stored icons live: an "icons" folder next to providers.json.
func iconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// sniffIcon names the image type of data by its content, or "" when it is
// not an image magpie accepts. The file's extension plays no part.
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

// StoreIcon saves an image under the icons folder and returns the reference
// to write into a provider's icon field ("file:<name>"). The name is derived
// from the content, so identical images are stored once and get the same
// reference.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes, over the 1 MB limit — use a smaller image", len(data))
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use: want PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext

	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored. Touch it so a prune running before the provider is
		// saved does not treat it as an old, unused file.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile is StoreIcon for an image on disk. The size is checked
// before the file is read, so a huge file is never loaded into memory.
func StoreIconFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a folder, not an image", path)
	}
	if fi.Size() > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes, over the 1 MB limit — use a smaller image", fi.Size())
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

// IconFile maps a stored icon's name (with or without the "file:" prefix) to
// its path on disk. Anything that is not a name StoreIcon could have produced
// returns "", so a reference can never reach outside the icons folder.
func IconFile(name string) string {
	name = strings.TrimPrefix(name, iconPrefix)
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// storeAndPrune writes the providers file, then clears out stored icons that
// nothing refers to any more.
func storeAndPrune(f file) error {
	if err := store(f); err != nil {
		return err
	}
	pruneIcons()
	return nil
}

// pruneIcons deletes stored icons that no provider's icon field refers to and
// that were last modified more than an hour ago. The hour gives an icon that
// was just stored, and not yet saved with its provider, time to be saved.
// Failures are ignored: a leftover icon is harmless and gets another chance
// on the next save.
func pruneIcons() {
	dir := iconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range load().Providers {
		if strings.HasPrefix(p.Icon, iconPrefix) {
			used[strings.TrimPrefix(p.Icon, iconPrefix)] = true
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
