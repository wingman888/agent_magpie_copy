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

// MaxIcon is the largest image magpie will keep as a provider icon.
const MaxIcon = 1 << 20 // 1 MB

// iconGrace is how long an unreferenced icon survives. It covers an image
// the user just picked but has not saved the provider for yet.
const iconGrace = time.Hour

// iconPrefix marks an icon value that points at a stored image.
const iconPrefix = "file:"

// iconNameRe is the only shape of stored icon file name: the first 8 bytes
// of the content's SHA-256 in hex, then an extension sniffed from the content.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// builtinIconRe is the shape of a name from the built-in logo set.
var builtinIconRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// iconDir is the icons folder beside providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile turns a stored file name (as written after "file:") into its path
// under the icons folder. Anything that is not exactly the shape StoreIcon
// produces returns "", so an icon value can never point outside that folder.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// sniffIcon names the image type of data by its content, or returns "" when
// it is not one of the supported formats.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}) && len(data) >= 6:
		return ".ico"
	case isSVG(data):
		return ".svg"
	}
	return ""
}

// isSVG reports whether the document's root element is <svg>. Prologue
// (XML declaration, comments, DOCTYPE) is skipped by the XML decoder.
func isSVG(data []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local == "svg"
		}
	}
}

// StoreIcon keeps an image under the icons folder and returns its icon value,
// "file:<name>". The name is derived from the content, so the same image is
// stored once; picking it again only refreshes its timestamp so it is not
// cleaned up before the provider is saved.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon: the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("icon: the image is %d bytes; the limit is %d", len(data), MaxIcon)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("icon: not an image (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconDir()
	path := filepath.Join(dir, name)

	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		_ = os.Chtimes(path, now, now)
		return iconPrefix + name, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// IconFromArg turns what the user typed for an icon into an icon value. A
// local image file is stored with StoreIcon; a bare word is a built-in logo
// name; "" clears the icon.
func IconFromArg(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if fi, err := os.Stat(arg); err == nil && fi.Mode().IsRegular() {
		f, err := os.Open(arg)
		if err != nil {
			return "", err
		}
		defer f.Close()
		// read one byte past the limit so an oversized file is reported, not truncated
		data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
		if err != nil {
			return "", err
		}
		return StoreIcon(data)
	}
	if strings.ContainsAny(arg, `/\`) || filepath.Ext(arg) != "" {
		return "", fmt.Errorf("icon: no such image file %q", arg)
	}
	if !builtinIconRe.MatchString(arg) {
		return "", fmt.Errorf("icon: %q is neither an image file nor a built-in icon name", arg)
	}
	return arg, nil
}

// checkIcon rejects an icon value that is neither a built-in name nor a
// stored image reference that IconFile accepts.
func checkIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if name, ok := strings.CutPrefix(icon, iconPrefix); ok {
		if IconFile(name) == "" {
			return fmt.Errorf("icon: %q is not a stored image", icon)
		}
		return nil
	}
	if !builtinIconRe.MatchString(icon) {
		return fmt.Errorf("icon: %q is not a valid icon name", icon)
	}
	return nil
}

// pruneIcons removes stored images that no provider refers to and that have
// not been written in over an hour. Files that do not have a stored-image
// name are left alone. Called by Save after the providers file is written.
func pruneIcons() {
	dir := iconDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range load().Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
