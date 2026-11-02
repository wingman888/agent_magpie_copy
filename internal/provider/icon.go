package provider

// Custom icons: a picture the user chose for one of their own providers.
// magpie keeps its own copy under the config directory, named after the
// picture's content, so moving or deleting the original changes nothing and
// the same picture is stored once. A provider refers to it as "file:<name>".

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

// MaxIcon is the largest picture magpie keeps as an icon.
const MaxIcon = 1 << 20

// IconPrefix marks an icon that is a picture the user supplied.
const IconPrefix = "file:"

// iconGrace is how long a stored picture nobody uses yet is kept, so one
// being edited in is not swept away before its provider is saved.
const iconGrace = time.Hour

var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is where stored pictures live: next to providers.json.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile returns where the stored picture called name lives, or "" when
// name is not one StoreIcon could have produced.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// iconExt tells the picture's type from its content.
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
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return ".ico"
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon keeps a picture and returns the reference to put in a
// provider's Icon.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d KB; pick a smaller image (at most 1 MB)", (len(data)+1023)/1024)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("unsupported image type: use a PNG, JPEG, GIF, WebP, ICO or SVG picture")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		now := time.Now()
		_ = os.Chtimes(path, now, now) // fresh again: not swept before it is saved
		return IconPrefix + name, nil
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
	return IconPrefix + name, nil
}

// StoreIconFile keeps the picture at path; see StoreIcon.
func StoreIconFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a picture", path)
	}
	if st.Size() > MaxIcon {
		return "", fmt.Errorf("the icon is %d KB; pick a smaller image (at most 1 MB)", (st.Size()+1023)/1024)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// sweepIcons removes stored pictures no provider in f refers to, once they
// are older than iconGrace. Failing to sweep is not an error worth reporting.
func sweepIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, IconPrefix); ok {
			used[name] = true
		}
	}
	dir := IconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		if e.IsDir() || used[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}
