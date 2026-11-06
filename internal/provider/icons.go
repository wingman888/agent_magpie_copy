package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registers the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // registers the JPEG decoder for image.DecodeConfig
	_ "image/png"  // registers the PNG decoder for image.DecodeConfig
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxIcon is the largest image that can be kept as a provider icon.
const MaxIcon = 1 << 20

// iconName is the only shape a stored icon file name takes: 8 bytes of the
// content's SHA-256 in hex, then the extension sniffed from the content.
var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

var svgTag = regexp.MustCompile(`(?i)<svg[\s>/]`)

// builtinIcons are the logos bundled with the GUI (internal/gui/assets/icons).
var builtinIcons = map[string]bool{
	"ai302-color": true, "aihubmix-color": true, "anthropic": true, "claude-color": true,
	"claudecode-color": true, "codex-color": true, "crush": true, "cursor": true,
	"deepseek-color": true, "fireworks-color": true, "gemini-color": true, "geminicli-color": true,
	"githubcopilot": true, "googlecloud-color": true, "goose": true, "groq": true,
	"kimi": true, "lmstudio": true, "minimax-color": true, "mistral-color": true,
	"moonshot": true, "ollama": true, "openai": true, "opencode": true,
	"openrouter": true, "pi": true, "qwen-color": true, "siliconcloud-color": true,
	"together-color": true, "xai": true, "yylx": true, "zai": true, "zhipu-color": true,
}

// BuiltinIcon reports whether name is one of the logos bundled with magpie.
func BuiltinIcon(name string) bool { return builtinIcons[name] }

// iconsDir is the icons folder inside magpie's config directory.
func iconsDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile maps a stored icon's file name to its path on disk. Anything
// that is not exactly such a name (a path, "..", another extension) gives "".
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// StoreIcon keeps an image as a provider icon and returns the reference to
// put in Provider.Icon ("file:<name>"). The same image is stored once; a
// stored image that is picked again has its timestamp refreshed so the
// next Save does not treat it as abandoned.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the icon is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the icon is %d KB; the limit is %d KB", len(data)>>10, MaxIcon>>10)
	}
	ext, err := sniffIcon(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	path := IconFile(name)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return "file:" + name, nil
	}
	if err := os.MkdirAll(iconsDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return "file:" + name, nil
}

// sniffIcon names the extension of an image by its content.
func sniffIcon(data []byte) (string, error) {
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		switch format {
		case "png":
			return ".png", nil
		case "jpeg":
			return ".jpg", nil
		case "gif":
			return ".gif", nil
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp", nil
	}
	if len(data) >= 6 && bytes.Equal(data[:4], []byte{0, 0, 1, 0}) && binary.LittleEndian.Uint16(data[4:6]) > 0 {
		return ".ico", nil
	}
	if isSVG(data) {
		return ".svg", nil
	}
	return "", errors.New("not an image magpie can show: PNG, JPEG, GIF, WebP, ICO or SVG")
}

// isSVG is true for UTF-8 text that opens an <svg> element near its start.
func isSVG(data []byte) bool {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	return svgTag.Match(head)
}

// pruneIcons deletes stored icons that no provider refers to and that are
// more than an hour old, so a picture chosen but not yet saved survives.
func pruneIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if n, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[n] = true
		}
	}
	dir := iconsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if e.IsDir() || !iconName.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
