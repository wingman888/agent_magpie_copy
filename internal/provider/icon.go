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
	"regexp"
	"strings"
	"time"
)

// A custom provider can wear the user's own picture. magpie keeps a copy
// under icons/ next to providers.json, named by its content, and the
// provider's icon field points at it as "file:<name>". Built-in icons stay
// plain names ("deepseek-color").

// MaxIcon is the largest picture magpie keeps as an icon.
const MaxIcon = 1 << 20

// IconPrefix marks an icon that is one of the user's pictures.
const IconPrefix = "file:"

// iconGrace is how long a stored picture nobody points at yet survives:
// the user may be halfway through editing the provider that will.
const iconGrace = time.Hour

var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is where the user's pictures are kept.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile is the path of a stored picture, given the name after "file:",
// or "" when the name is not one StoreIcon makes.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// iconType tells the picture's kind from its bytes, as a file extension.
func iconType(b []byte) (string, error) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", nil
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return ".gif", nil
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp", nil
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return ".ico", nil
	case bytes.Contains(bytes.ToLower(b), []byte("<svg")):
		return ".svg", nil
	}
	return "", errors.New("not an image magpie can use as an icon: use a PNG, JPEG, GIF, WebP, ICO or SVG file")
}

// StoreIcon keeps a picture and returns the reference for a provider's
// icon field ("file:<sha>.<ext>"). The same picture is kept once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon image is %d KB; the limit is 1 MB, pick a smaller one", (len(data)+1023)/1024)
	}
	ext, err := iconType(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		// already kept; freshen it so the clean-up leaves it be while the
		// user finishes editing
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return IconPrefix + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	_ = os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return IconPrefix + name, nil
}

// StoreIconFile keeps the picture in a file on disk; see StoreIcon.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no such file: %s", path)
		}
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder, not an image", path)
	}
	if st.Size() > MaxIcon {
		return "", fmt.Errorf("%s is %d KB; the limit is 1 MB, pick a smaller image", path, (st.Size()+1023)/1024)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	ref, err := StoreIcon(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return ref, nil
}

// iconRef is the stored picture an icon field points at, or "".
func iconRef(icon string) string {
	if name, ok := strings.CutPrefix(icon, IconPrefix); ok && iconNameRe.MatchString(name) {
		return name
	}
	return ""
}

// cleanIcons removes the stored pictures no provider points at any more,
// sparing the ones kept within the last hour.
func cleanIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if n := iconRef(p.Icon); n != "" {
			used[n] = true
		}
	}
	dir := IconDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || used[n] || !iconNameRe.MatchString(n) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, n))
	}
}
