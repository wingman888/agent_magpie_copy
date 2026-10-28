package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A provider's own picture lives in the icons folder beside providers.json,
// named after its content, and the provider's Icon reads "file:<name>". The
// built-in icons are plain names ("deepseek-color").
const iconPrefix = "file:"

// MaxIcon is the largest picture magpie keeps for a provider.
const MaxIcon = 1 << 20

var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is where pictures given to providers are kept.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile is the path of a stored picture, for a name as StoreIcon made it;
// "" for anything else, so a request can't reach outside the folder.
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// StoreIcon keeps a picture (PNG, JPEG, GIF, WebP, ICO or SVG) and returns
// the Icon value that points at it. The same picture is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the picture is empty")
	}
	if len(data) > MaxIcon {
		return "", errors.New("the picture is over 1 MB; pick a smaller one")
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not a picture magpie can show: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	if err := os.MkdirAll(IconDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(IconDir(), name), data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// StoreIconFile is StoreIcon for a picture on disk.
func StoreIconFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() > MaxIcon {
		return "", errors.New("the picture is over 1 MB; pick a smaller one")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(b)
}

func iconExt(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico"
	}
	head := bytes.ToLower(b[:min(len(b), 1024)])
	if bytes.Contains(head, []byte("<svg")) {
		return "svg"
	}
	return ""
}

// pruneIcons removes pictures no provider points at any more. One picked in
// the editor is stored before its provider is saved, so a fresh one stays.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	ents, _ := os.ReadDir(IconDir())
	for _, e := range ents {
		if !iconName.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			os.Remove(filepath.Join(IconDir(), e.Name()))
		}
	}
}
