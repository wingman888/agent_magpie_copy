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

// A custom provider can carry its own logo. The picture is copied into the
// icons folder next to providers.json, named after its content, and the
// provider stores "file:<name>". Only that exact name shape ever resolves to
// a path, so an icon reference cannot reach anything outside the folder.

// MaxIcon is the largest picture accepted as an icon.
const MaxIcon = 1 << 20

// iconPrefix marks a provider icon that refers to a stored file.
const iconPrefix = "file:"

// iconGrace is how long an unreferenced picture is kept, so one just chosen
// in the editor survives until the provider is saved.
const iconGrace = time.Hour

var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir is the icons folder, beside providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// sniffIcon names the extension of a picture by its content, or "" when it
// is not one of the supported formats.
func sniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
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

// isSVG is true when the document's root element is <svg>. It reads tokens
// only as far as the first element, so no entity or DTD is expanded.
func isSVG(data []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
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

// StoreIcon saves a picture into the icons folder and returns the icon value
// that refers to it ("file:<name>"). The same content is stored once; an
// existing copy has its time refreshed so the cleanup leaves it alone.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is larger than %d KB", MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("the file is not an image (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	now := time.Now()
	if _, err := os.Stat(path); err == nil {
		_ = os.Chtimes(path, now, now)
		return iconPrefix + name, nil
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
	return iconPrefix + name, nil
}

// IconFile turns a stored icon's file name into its path on disk. Anything
// that is not exactly "<16 hex digits>.<known extension>" gives "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// pruneIcons deletes stored pictures that no provider refers to and that have
// not been written for over an hour. Files that do not have the stored name
// shape are not ours to remove and are left alone.
func pruneIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	entries, err := os.ReadDir(iconDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !iconNameRe.MatchString(name) || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(iconDir(), name))
	}
}

// StoreIconFile is StoreIcon for a picture on disk. It reads at most one
// byte past MaxIcon, so a huge file is refused without being loaded whole.
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
