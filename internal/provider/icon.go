package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest picture magpie will keep as a provider icon.
const MaxIcon = 1 << 20

// iconGrace is how long an icon nobody refers to yet is kept: long enough
// for the user to pick it and save the provider.
const iconGrace = time.Hour

// iconNameRe is the only shape of icon file name magpie reads: 8 bytes of the
// content's SHA-256 in hex, then the extension its content was recognised as.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir is the icons folder next to providers.json.
func iconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// StoreIcon saves a picture into the icons folder and returns the icon value
// that refers to it ("file:<name>"). The same picture is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d bytes; the limit is 1 MB", len(data))
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("the icon is not an image magpie can show (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	path := IconFile(name)
	if err := os.MkdirAll(iconDir(), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		// Already stored: touch it so it is protected by the grace period again.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// IconFile turns a stored icon's file name into its path on disk. Anything
// that is not exactly a name StoreIcon could have produced gives "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// sniffIcon names the extension for a picture's content, or "" if it is not
// one of the formats magpie shows.
func sniffIcon(data []byte) string {
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

// isSVG is true when the first element of the document is <svg>.
func isSVG(data []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for i := 0; i < 64; i++ {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "svg"
		}
	}
	return false
}

// cleanIcons removes stored icons that no saved provider refers to and that
// are older than the grace period. Anything not named like a stored icon is
// left alone.
func cleanIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
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
		if e.IsDir() || used[name] || IconFile(name) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(iconDir(), name))
	}
}
