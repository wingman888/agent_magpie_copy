package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxIcon is the largest icon image magpie keeps.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced icon survives, so a freshly chosen
// image outlives the moment before its provider is saved.
const iconGrace = time.Hour

var iconExts = map[string]bool{".png": true, ".jpg": true, ".gif": true, ".webp": true, ".ico": true, ".svg": true}

// IconDir is where icon images live.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// sniffIcon names the extension for an image's bytes, or "".
func sniffIcon(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif"
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return ".ico"
	}
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	t := bytes.TrimSpace(head)
	if len(t) > 0 && t[0] == '<' && bytes.Contains(bytes.ToLower(t), []byte("<svg")) && utf8.Valid(b) {
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image under the icons folder and returns the icon
// value ("file:<name>") to put on a provider. The same image is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d KB; the limit is %d KB", len(data)>>10, MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not an image magpie can use (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		now := time.Now()
		os.Chtimes(path, now, now) // chosen again: it is fresh again
		return "file:" + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return "file:" + name, nil
}

// IconFile is the path of a stored icon, given its file name; "" when the
// name is not one StoreIcon makes (16 hex digits and a known extension).
func IconFile(name string) string {
	ext := filepath.Ext(name)
	if !iconExts[ext] || len(name) != 16+len(ext) {
		return ""
	}
	for _, c := range name[:16] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return filepath.Join(IconDir(), name)
}

// storeAndClean writes the file, then drops icons nothing refers to.
func storeAndClean(f file) error {
	if err := store(f); err != nil {
		return err
	}
	cleanIcons(f)
	return nil
}

func cleanIcons(f file) {
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if n, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[n] = true
		}
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		if e.IsDir() || used[e.Name()] || IconFile(e.Name()) == "" {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(IconDir(), e.Name()))
		}
	}
}
