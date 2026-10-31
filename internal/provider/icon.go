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
)

// MaxIcon is the largest custom icon magpie keeps.
const MaxIcon = 1 << 20

// iconKeep is how long an unreferenced icon survives, so one stored while
// the user is still editing is not swept away.
const iconKeep = time.Hour

var iconExts = []string{".png", ".jpg", ".gif", ".webp", ".ico", ".svg"}

// IconsDir is where the user's own icon images live, next to providers.json.
func IconsDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

func iconExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return ".ico"
	case bytes.Contains(bytes.ToLower(data), []byte("<svg")):
		return ".svg"
	}
	return ""
}

// StoreIcon saves an image and returns the "file:" reference to put in a
// provider's Icon. The same bytes always give the same reference.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon image is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon image is %d KB; pick one under %d KB", (len(data)+1023)/1024, MaxIcon/1024)
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("the icon is not a PNG, JPEG, GIF, WebP, ICO or SVG image")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])[:16] + ext
	dir := IconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		// a re-chosen icon counts as fresh
		now := time.Now()
		os.Chtimes(path, now, now)
		return "file:" + name, nil
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
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

// StoreIconFile is StoreIcon for an image on disk.
func StoreIconFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder, not an image", path)
	}
	if st.Size() > MaxIcon {
		return "", fmt.Errorf("%s is %d KB; pick an icon under %d KB", path, (st.Size()+1023)/1024, MaxIcon/1024)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(data)
}

func validIconName(name string) bool {
	if len(name) < 16 {
		return false
	}
	stem, ext := name[:16], name[16:]
	ok := false
	for _, e := range iconExts {
		if ext == e {
			ok = true
		}
	}
	if !ok {
		return false
	}
	for _, c := range stem {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IconFile maps the file name after "file:" to the image on disk, or ""
// when the name is not one StoreIcon makes.
func IconFile(name string) string {
	if !validIconName(name) {
		return ""
	}
	return filepath.Join(IconsDir(), name)
}

// sweepIcons deletes stored images no provider uses and that are older
// than an hour.
func sweepIcons(ps []Provider) {
	ents, err := os.ReadDir(IconsDir())
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, p := range ps {
		if n, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[n] = true
		}
	}
	cutoff := time.Now().Add(-iconKeep)
	for _, e := range ents {
		if e.IsDir() || !validIconName(e.Name()) || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(IconsDir(), e.Name()))
		}
	}
}
