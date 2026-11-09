package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest image a provider icon may be.
const MaxIcon = 1 << 20

// iconPrefix marks an Icon value that names a stored image file, as opposed
// to a built-in icon name.
const iconPrefix = "file:"

// iconGrace is how long an unreferenced image survives a save, so an image
// the user just picked but has not saved yet is not deleted under them.
const iconGrace = time.Hour

// iconNameRe is the only shape of file name magpie will ever read or delete:
// 8 bytes of SHA-256 in hex, then one of the extensions iconExt produces.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is the icons folder inside magpie's config directory.
func IconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// StoreIcon saves an image under icons/ and returns the Icon value that
// refers to it ("file:<name>"). The name is derived from the content, so the
// same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is larger than %d KB", MaxIcon>>10)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use: PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	if err := os.MkdirAll(IconDir(), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(IconDir(), name)
	if _, err := os.Stat(path); err == nil {
		// Already stored. Touch it so it counts as recent again, in case
		// it was unreferenced and is about to be pruned.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// IconFile turns a stored file name into its path on disk. Anything that is
// not exactly a name StoreIcon produces yields "", so an icon reference can
// never reach outside icons/.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// checkIcon rejects a "file:" reference that IconFile would not resolve.
// Built-in names are not checked here.
func checkIcon(icon string) error {
	if strings.HasPrefix(icon, iconPrefix) && IconFile(strings.TrimPrefix(icon, iconPrefix)) == "" {
		return fmt.Errorf("icon %q is not a stored image", icon)
	}
	return nil
}

// pruneIcons deletes stored images no provider refers to and that are older
// than the grace period. Only files whose names match iconNameRe are touched.
func pruneIcons(ps []Provider) {
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range ps {
		if strings.HasPrefix(p.Icon, iconPrefix) {
			used[strings.TrimPrefix(p.Icon, iconPrefix)] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(IconDir(), name))
	}
}

// iconExt names the image type by its content, or returns "" when the bytes
// are none of the supported formats.
func iconExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case isSVG(data):
		return ".svg"
	}
	return ""
}

// isSVG reports whether the document's root element is <svg>.
func isSVG(data []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "svg"
		}
	}
}
