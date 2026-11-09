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
	"unicode/utf8"
)

// MaxIcon is the largest image magpie keeps as a custom vendor's icon.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced icon survives. A picture the user
// just chose, but has not saved a provider with yet, must not be swept.
const iconGrace = time.Hour

// iconRefPrefix marks an icon value that points at a stored image.
const iconRefPrefix = "file:"

// iconNameRe is the only shape of stored icon file name IconFile accepts:
// 16 lowercase hex digits (the first 8 bytes of the content's SHA-256), a dot,
// and one of the extensions iconExt can produce. Nothing else can name a path.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// builtinIconRe is the shape of a built-in icon name, such as "openai" or
// "claude-color".
var builtinIconRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// iconsDir is the icons folder next to providers.json.
func iconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile turns a stored icon's file name into its path on disk. Anything
// that is not exactly a name StoreIcon could have produced yields "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// iconExt names the image format by its content, or "" when it is none
// magpie shows.
func iconExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case utf8.Valid(data) && bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon keeps an image in the icons folder and returns the icon value
// that refers to it ("file:<name>"). The name comes from the content, so the
// same picture is stored once; storing it again just refreshes its age.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the image is %d bytes; icons are limited to %d", len(data), MaxIcon)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not an image magpie can show (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconsDir()
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return iconRefPrefix + name, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return iconRefPrefix + name, nil
}

// CheckIcon reports whether an icon value is acceptable: "" (the generic
// icon), a built-in name, or a file:<name> reference to an image that is
// stored. A reference never reaches outside the icons folder.
func CheckIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if strings.HasPrefix(icon, iconRefPrefix) {
		path := IconFile(strings.TrimPrefix(icon, iconRefPrefix))
		if path == "" {
			return fmt.Errorf("%q is not an icon magpie stored", icon)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%q: the image is no longer stored", icon)
		}
		return nil
	}
	if !builtinIconRe.MatchString(icon) {
		return fmt.Errorf("%q is neither an image file nor a built-in icon name", icon)
	}
	return nil
}

// SetIcon sets a custom vendor's icon. Presets and signed-in accounts keep
// their own. "" restores the generic icon.
func SetIcon(id, icon string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Preset != "" || p.Account != nil {
		return fmt.Errorf("%s has its own icon; only custom providers can set one", p.Name)
	}
	icon = strings.TrimSpace(icon)
	if err := CheckIcon(icon); err != nil {
		return err
	}
	p.Icon = icon
	return Save(*p)
}

// pruneIcons deletes stored images no provider refers to and that are older
// than iconGrace. It runs after providers.json is written.
func pruneIcons(ps []Provider) {
	refs := map[string]bool{}
	for _, p := range ps {
		if strings.HasPrefix(p.Icon, iconRefPrefix) {
			refs[strings.TrimPrefix(p.Icon, iconRefPrefix)] = true
		}
	}
	dir := iconsDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range ents {
		if !e.Type().IsRegular() || refs[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
