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
	"time"
)

// MaxIcon is the largest image magpie will store as a provider icon: 1 MB.
const MaxIcon = 1 << 20

// iconPruneAge is how long an unreferenced icon is kept after it was last
// written, so a file stored while the user is still editing survives.
const iconPruneAge = time.Hour

// iconExts maps the extension a stored icon gets to the content type it
// was sniffed as. The set is also what IconFile accepts.
var iconExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".gif":  true,
	".webp": true,
	".ico":  true,
	".svg":  true,
}

// iconDir is the directory stored icons live in: "icons" next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// iconSniff returns the extension for the image type of data, or an error
// when the content is not one of the supported types.
func iconSniff(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", nil
	case bytes.HasPrefix(data, []byte("\xFF\xD8\xFF")):
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
	return "", errors.New("unsupported icon type: want PNG, JPEG, GIF, WebP, ICO or SVG")
}

// validIconName reports whether name is exactly the form StoreIcon produces:
// 16 lowercase hex digits, a dot, and one of the known extensions.
func validIconName(name string) bool {
	if len(name) <= 16 || !iconExts[name[16:]] {
		return false
	}
	for _, c := range name[:16] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// StoreIcon saves an image and returns the reference to put in a provider's
// icon field ("file:<name>"). Identical content gets the same reference and
// is stored only once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes, over the 1 MB limit; choose a smaller image", len(data))
	}
	ext, err := iconSniff(data)
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
	if _, err := os.Stat(p); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	}
	return "file:" + name, nil
}

// StoreIconFile saves the image at path (see StoreIcon). It reads at most
// MaxIcon+1 bytes, so an oversized file is rejected without being loaded.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// IconFile returns the path of the stored image named by name (the part of
// an icon reference after "file:"), or "" if name is not a stored-icon name.
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// pruneIcons deletes stored icons that no provider references and that were
// last modified more than an hour before now. refs holds the provider icon
// values in use ("file:<name>" entries matter; built-in names are ignored).
func pruneIcons(refs map[string]bool, now time.Time) error {
	dir := iconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := now.Add(-iconPruneAge)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !validIconName(name) || refs["file:"+name] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
