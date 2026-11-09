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

// MaxIcon is the largest picture magpie will keep as a provider icon.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced icon survives, so a picture chosen
// for a provider that is not saved yet is not swept away.
const iconGrace = time.Hour

// iconNameRe is the only shape of icon file name magpie accepts: the first
// 8 bytes of the content's SHA-256 in hex, then one of the known extensions.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconsDir is the folder, beside providers.json, that holds icon pictures.
func IconsDir() string {
	return filepath.Join(filepath.Dir(Path()), "icons")
}

// IconFile turns a stored icon file name into its path on disk. Anything
// that is not exactly a name StoreIcon could have produced yields "", so a
// stored reference can never point outside the icons folder.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconsDir(), name)
}

// iconExt recognises the picture format from its content, or returns "".
func iconExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP":
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon saves a picture under icons/ and returns the icon value to keep
// on a provider, "file:<name>". The same picture is stored once; storing it
// again refreshes its timestamp so the grace period starts over.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is larger than %d KB", MaxIcon>>10)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not a picture: magpie takes PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	path := IconFile(name)
	if err := os.MkdirAll(IconsDir(), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// pruneIcons deletes icon pictures no provider refers to, once they are
// older than the grace period. Only files with the exact icon name shape
// are touched.
func pruneIcons() {
	dir := IconsDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range load().Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || used[name] || !iconNameRe.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
