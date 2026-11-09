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

// MaxIcon is the largest image magpie will keep as a provider icon: 1 MB.
const MaxIcon = 1 << 20

// iconPrefix marks an Icon value that names a stored image rather than a
// built-in icon name.
const iconPrefix = "file:"

// iconMinAge is how long a stored image is kept before it may be pruned,
// so an icon stored during an edit survives until the provider is saved.
const iconMinAge = time.Hour

// iconNameRe is the only shape of stored icon file name magpie accepts:
// 16 lowercase hex digits (the start of the content's SHA-256) plus the
// extension for the detected image type.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir is the directory stored icons live in: "icons" next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// iconType detects an image's type from its content and returns the file
// extension for it, or "" if it is not a supported image.
func iconType(data []byte) string {
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
	case bytes.Contains(data, []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image in the icons directory and returns the "file:"
// reference to put in a provider's icon field. The file is named by the
// content's SHA-256, so the same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes; the limit is 1 MB, try a smaller image", len(data))
	}
	ext := iconType(data)
	if ext == "" {
		return "", errors.New("unsupported image type: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored: keep the content, but refresh the time so it
		// counts as just stored and survives pruning for the usual hour.
		now := time.Now()
		os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile is StoreIcon for an image on disk. It refuses to read more
// than MaxIcon bytes, so a huge file is rejected without loading it.
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
		return "", fmt.Errorf("icon is %d bytes; the limit is 1 MB, try a smaller image", st.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// IconFile maps the file name from a "file:" reference to the image's path
// on disk. Anything that is not a name StoreIcon produces yields "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// storeAndPrune writes the providers file, then drops stored icons nothing
// refers to any more.
func storeAndPrune(f file) error {
	if err := store(f); err != nil {
		return err
	}
	pruneIcons()
	return nil
}

// pruneIcons deletes stored icons that no provider's icon field references
// and that are more than an hour old. Errors are ignored: pruning is
// housekeeping and must not fail a save.
func pruneIcons() {
	dir := iconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range load().Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconMinAge)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
