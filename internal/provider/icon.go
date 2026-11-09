package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Custom providers may carry their own picture. The picture lives in the
// icons folder next to providers.json, named after its content, and the
// provider refers to it as "file:<name>". A built-in icon is just its name.

// MaxIcon is the largest picture kept as a provider icon.
const MaxIcon = 1 << 20

// magIconRefPrefix marks an icon value that points at a stored picture.
const magIconRefPrefix = "file:"

// magIconGrace is how long an unreferenced picture survives: long enough for a
// picture chosen in the UI to be saved with its provider.
const magIconGrace = time.Hour

// magIconNameRe is the only shape of stored picture name IconFile accepts.
var magIconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is the folder pictures are stored in.
func IconDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile turns a stored picture's name into its path on disk. Anything
// that is not exactly a name StoreIcon could have produced gives "".
func IconFile(name string) string {
	if !magIconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// StoreIcon keeps a picture for use as a provider icon and returns the icon
// value to save ("file:<name>"). A picture already stored is not written
// twice; its timestamp is refreshed so pruning leaves it alone for a while.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is larger than %d KB", MaxIcon>>10)
	}
	ext := magSniffIcon(data)
	if ext == "" {
		return "", errors.New("the icon is not a PNG, JPEG, GIF, WebP, ICO or SVG picture")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	if IconFile(name) == "" {
		return "", fmt.Errorf("cannot store icon %q", name)
	}
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	now := time.Now()
	if _, err := os.Stat(p); err == nil {
		_ = os.Chtimes(p, now, now)
		return magIconRefPrefix + name, nil
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return magIconRefPrefix + name, nil
}

// magSniffIcon names the picture format from its leading bytes, or "" when the
// data is none of the formats magpie takes.
func magSniffIcon(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return "ico"
	case magIsSVG(data):
		return "svg"
	}
	return ""
}

// magIsSVG is true when the text opens with an <svg> element, or with an XML
// declaration, comment or doctype and then an <svg> element.
func magIsSVG(data []byte) bool {
	t := bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	t = bytes.TrimLeft(t, " \t\r\n")
	lower := bytes.ToLower(t)
	if bytes.HasPrefix(lower, []byte("<svg")) {
		return true
	}
	for _, p := range [][]byte{[]byte("<?xml"), []byte("<!--"), []byte("<!doctype svg")} {
		if bytes.HasPrefix(lower, p) {
			return bytes.Contains(lower, []byte("<svg"))
		}
	}
	return false
}

// BuiltinIcon reports whether a name is one of the logos bundled with magpie.
func BuiltinIcon(name string) bool {
	if name == "" {
		return false
	}
	for _, pr := range Presets() {
		if pr.Icon == name {
			return true
		}
	}
	return false
}

// magIconRefName returns the stored picture's name when an icon value refers to
// one, and whether that reference resolves to a legal file.
func magIconRefName(icon string) (string, bool) {
	name, ok := strings.CutPrefix(icon, magIconRefPrefix)
	if !ok {
		return "", false
	}
	return name, IconFile(name) != ""
}

// magPruneIcons deletes stored pictures that no provider in f refers to and
// that have not been touched within magIconGrace.
func magPruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := magIconRefName(p.Icon); ok {
			used[name] = true
		}
	}
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || used[name] || IconFile(name) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < magIconGrace {
			continue
		}
		os.Remove(filepath.Join(IconDir(), name))
	}
}
