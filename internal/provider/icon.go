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
)

// MaxIcon is the largest image magpie will accept as a provider icon (1 MB).
// Anything strictly larger is refused.
const MaxIcon = 1 << 20

// iconPrefix marks an Icon value that refers to a stored user image.
const iconPrefix = "file:"

// iconDir is the directory user icons live in: an "icons" folder next to
// providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// iconExt identifies the image type from the content (never the file name).
// It returns the file extension for the type, or an error when the content
// is not one of the supported formats.
func iconExt(data []byte) (string, error) {
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
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg", nil
	}
	return "", errors.New("unsupported image type: want PNG, JPEG, GIF, WebP, ICO or SVG")
}

// StoreIcon saves an image under the icons directory and returns the
// reference to write into a provider's icon field ("file:<name>"). The name
// is the first 16 hex digits of the content's SHA-256 plus the type's
// extension, so identical content is stored once and yields the same
// reference.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon is %d bytes, larger than the 1 MB limit; use a smaller image", len(data))
	}
	ext, err := iconExt(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := writeIconFile(dst, data); err != nil {
			return "", err
		}
	}
	return iconPrefix + name, nil
}

// writeIconFile writes via a temporary file and a rename, so a crash never
// leaves a half-written image under the final name.
func writeIconFile(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".icon-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// StoreIconFile reads the image at path and stores it with StoreIcon. The
// file's content, not its extension, decides the type. Reading stops just
// past MaxIcon so an oversized file is never loaded whole.
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

// IconFile maps the file name from an "file:<name>" icon reference to the
// image's path on disk. Only names StoreIcon can produce are accepted: 16
// lowercase hex digits followed by one of the known extensions. Anything
// else, including path separators or "..", returns "".
func IconFile(name string) string {
	ext := filepath.Ext(name)
	switch ext {
	case ".png", ".jpg", ".gif", ".webp", ".ico", ".svg":
	default:
		return ""
	}
	base := strings.TrimSuffix(name, ext)
	if len(base) != 16 {
		return ""
	}
	for _, c := range base {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return ""
		}
	}
	return filepath.Join(iconDir(), name)
}
