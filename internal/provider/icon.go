package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest picture a provider icon may be.
const MaxIcon = 1 << 20

// iconPrefix marks an Icon that is a stored picture, not a built-in name.
const iconPrefix = "file:"

var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is where custom provider pictures live, next to providers.json.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile is the path of a stored picture, or "" when name is not one
// StoreIcon made.
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

func iconExt(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "ico"
	}
	head := b[:min(len(b), 1024)]
	if bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return "svg"
	}
	return ""
}

var errIconBig = errors.New("the picture is over 1 MB; pick a smaller one")

// StoreIcon saves a picture and returns the Icon reference for it.
func StoreIcon(data []byte) (string, error) {
	switch {
	case len(data) == 0:
		return "", errors.New("the picture is empty")
	case len(data) > MaxIcon:
		return "", errIconBig
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not a picture magpie can show: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	path := IconFile(name)
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(data)) {
		// same picture again: count it as fresh so a sweep leaves it for the save that follows
		now := time.Now()
		os.Chtimes(path, now, now)
		return iconPrefix + name, nil
	}
	if err := os.MkdirAll(IconDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile saves the picture at path; see StoreIcon.
func StoreIconFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxIcon+1))
	if err != nil {
		return "", err
	}
	return StoreIcon(b)
}

// sweepIcons deletes stored pictures no provider uses any more. Ones under
// an hour old stay: an editor may store a picture before saving the provider.
func sweepIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if n, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[n] = true
		}
	}
	ents, err := os.ReadDir(IconDir())
	if err != nil {
		return
	}
	for _, e := range ents {
		if !iconNameRe.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > time.Hour {
			os.Remove(filepath.Join(IconDir(), e.Name()))
		}
	}
}

// storeAndSweep writes the providers, then clears out unused pictures.
func storeAndSweep(f file) error {
	if err := store(f); err != nil {
		return err
	}
	sweepIcons(f.Providers)
	return nil
}
