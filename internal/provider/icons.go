package provider

// Icons a custom provider can wear. A picture is stored once, named by its
// content, in icons/ beside providers.json; the provider refers to it as
// "file:<name>". A value without that prefix is a built-in icon name.

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

// iconGrace is how long an unreferenced picture survives cleanup, so one
// chosen a moment ago is not removed before its provider is saved.
const iconGrace = time.Hour

var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconsDir is the folder the pictures live in.
func IconsDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile maps a stored picture name to its path on disk. Anything that is
// not exactly a name StoreIcon produces (a path, "..", another extension,
// the empty string) gives "", so a reference can never reach outside icons/.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconsDir(), name)
}

// StoreIcon keeps a picture and returns the value a provider stores for it,
// "file:<name>". The same picture is only ever kept once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d bytes; the limit is %d", len(data), MaxIcon)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not an image magpie can show: PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	path := IconFile(name)
	if _, err := os.Stat(path); err == nil {
		// already kept: refresh its time so a cleanup running before the
		// provider is saved leaves it alone
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			return "", err
		}
		return "file:" + name, nil
	}
	if err := os.MkdirAll(IconsDir(), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(IconsDir(), "icon-*.tmp")
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// iconExt names the format of a picture by its content, or "" if it is none
// magpie shows.
func iconExt(data []byte) string {
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

// isSVG looks at the start of a text picture: an <svg> root, or an XML
// declaration followed by an <svg> root within the first few kilobytes.
func isSVG(data []byte) bool {
	head := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	head = bytes.TrimLeft(head, " \t\r\n")
	switch {
	case bytes.HasPrefix(head, []byte("<svg")):
		return true
	case bytes.HasPrefix(head, []byte("<?xml")):
		if len(head) > 4096 {
			head = head[:4096]
		}
		return bytes.Contains(head, []byte("<svg"))
	}
	return false
}

// validIcon rejects an icon value that could not be shown or that points
// somewhere other than icons/.
func validIcon(v string) error {
	if name, ok := strings.CutPrefix(v, "file:"); ok {
		if IconFile(name) == "" {
			return fmt.Errorf("%q is not an icon magpie stored", v)
		}
		return nil
	}
	if strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
		return fmt.Errorf("%q is not an icon name", v)
	}
	return nil
}

// pruneIcons removes stored pictures that no provider refers to and that
// have not been touched for about an hour. Called after a provider is saved.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	ents, err := os.ReadDir(IconsDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range ents {
		name := e.Name()
		if IconFile(name) == "" || used[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(IconFile(name))
	}
}
