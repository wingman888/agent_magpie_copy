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

// MaxIcon is the largest custom icon magpie keeps.
const MaxIcon = 1 << 20

const iconPrefix = "file:"

// iconKeep is how long a stored image nobody references is kept, so one
// that was just stored but not yet saved with its provider survives.
const iconKeep = time.Hour

// now is the clock the icon sweep reads; tests replace it.
var now = time.Now

var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// iconDir sits next to the providers file.
func iconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// sniffIcon names the image type of data by its content: the file suffix.
func sniffIcon(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", true
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp", true
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}) && len(data) >= 6 && (data[4] != 0 || data[5] != 0):
		return "ico", true
	}
	if isSVG(data) {
		return "svg", true
	}
	return "", false
}

// isSVG skips a BOM, whitespace, an XML declaration, comments and a doctype,
// then wants an <svg element.
func isSVG(data []byte) bool {
	b := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for {
		b = bytes.TrimLeft(b, " \t\r\n")
		switch {
		case bytes.HasPrefix(b, []byte("<?")):
			i := bytes.Index(b, []byte("?>"))
			if i < 0 {
				return false
			}
			b = b[i+2:]
		case bytes.HasPrefix(b, []byte("<!--")):
			i := bytes.Index(b, []byte("-->"))
			if i < 0 {
				return false
			}
			b = b[i+3:]
		case len(b) >= 9 && strings.EqualFold(string(b[:9]), "<!doctype"):
			// a doctype may hold an [internal subset] with its own '>'
			depth := 0
			i := 0
			for ; i < len(b); i++ {
				if b[i] == '[' {
					depth++
				} else if b[i] == ']' {
					depth--
				} else if b[i] == '>' && depth <= 0 {
					break
				}
			}
			if i >= len(b) {
				return false
			}
			b = b[i+1:]
		default:
			if len(b) < 5 || !strings.EqualFold(string(b[:4]), "<svg") {
				return false
			}
			switch b[4] {
			case ' ', '\t', '\r', '\n', '>', '/':
				return true
			}
			return false
		}
	}
}

// StoreIcon keeps an image in the icons folder and returns the reference
// ("file:<name>") a provider's Icon can hold. The same bytes give the same
// reference and one file.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon file is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %.1f MB; pick one smaller than %d MB", float64(len(data))/(1<<20), MaxIcon>>20)
	}
	ext, ok := sniffIcon(data)
	if !ok {
		return "", errors.New("the icon is not an image magpie can use (PNG, JPEG, GIF, WebP, ICO or SVG)")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	dir := iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		t := now()
		_ = os.Chtimes(path, t, t) // fresh again, so a sweep before its provider is saved spares it
		return iconPrefix + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if t := now(); true {
		_ = os.Chtimes(path, t, t)
	}
	return iconPrefix + name, nil
}

// StoreIconFile is StoreIcon for an image on disk.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a folder, not an image", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

// IconFile is where a stored icon lives, given the name after "file:".
// It returns "" for anything StoreIcon would not have produced.
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(iconDir(), name)
}

// sweepIcons deletes stored images that no provider in f references and
// that were stored more than an hour ago.
func sweepIcons(f file) {
	entries, err := os.ReadDir(iconDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range f.Providers {
		if strings.HasPrefix(p.Icon, iconPrefix) {
			used[strings.TrimPrefix(p.Icon, iconPrefix)] = true
		}
	}
	t := now()
	for _, e := range entries {
		if e.IsDir() || !iconName.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || t.Sub(info.ModTime()) < iconKeep {
			continue
		}
		os.Remove(filepath.Join(iconDir(), e.Name()))
	}
}

// SetIcon points a custom provider at an image file, a built-in icon name,
// or (with "") back at the default. It returns the icon value now in effect.
func SetIcon(id, arg string) (string, error) {
	p, err := Find(id)
	if err != nil {
		return "", err
	}
	if p.Account != nil || p.Preset != "" {
		return "", fmt.Errorf("%s keeps its own icon; only custom providers can have one set", p.Name)
	}
	icon, err := ResolveIcon(arg)
	if err != nil {
		return "", err
	}
	p.Icon = icon
	if err := Save(*p); err != nil {
		return "", err
	}
	return icon, nil
}

// ResolveIcon turns what the user typed — an image path, a built-in icon
// name, or "" — into a value for Provider.Icon.
func ResolveIcon(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		return StoreIconFile(arg)
	}
	if strings.HasPrefix(arg, iconPrefix) || strings.ContainsAny(arg, `/\`) {
		return "", fmt.Errorf("%q is neither an image file that exists nor a built-in icon name", arg)
	}
	return arg, nil
}
