package provider

// Provider icons. A custom provider can show a picture the user chose: magpie
// copies it into the icons folder next to providers.json, named by its
// content, and the provider refers to it as "file:<name>". Nothing else is
// ever read through an icon reference: IconFile is the only way from a name
// to a path, and it accepts only names magpie itself writes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxIcon is the largest picture kept as an icon.
const MaxIcon = 1 << 20

// iconGrace is how long an unreferenced picture is kept. A picture is stored
// as soon as it is chosen, but the provider only refers to it once saved.
const iconGrace = time.Hour

// iconNameRe is the only shape of icon file name magpie writes: the first
// eight bytes of the content's SHA-256 in lowercase hex, and an extension
// chosen by the content.
var iconNameRe = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// builtinIcons are the logos bundled with magpie, by name.
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

// BuiltinIcon reports whether name is a logo bundled with magpie.
func BuiltinIcon(name string) bool { return builtinIcons[name] }

// iconsDir is the folder the pictures live in.
func iconsDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile maps a stored picture's file name to its path on disk. Anything
// that is not exactly a name StoreIcon produces — a path, "..", a wrong
// extension, the empty string — gives "".
func IconFile(name string) string {
	if !iconNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(iconsDir(), name)
}

// StoreIcon keeps a picture for use as a provider icon and returns the
// reference to save on the provider ("file:<name>"). The same picture is
// stored once. Only PNG, JPEG, GIF, WebP, ICO and SVG are accepted, judged by
// their content, not their name.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the picture is empty")
	}
	if len(data) > MaxIcon {
		return "", fmt.Errorf("the picture is larger than %d KB", MaxIcon>>10)
	}
	ext := sniffIcon(data)
	if ext == "" {
		return "", errors.New("not a picture magpie can use: PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		// Already stored. Touch it, so it counts as freshly chosen.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return "file:" + name, nil
	}
	// Write under a temporary name and rename, so a half-written picture is
	// never visible under its final name.
	tmp, err := os.CreateTemp(dir, ".icon-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return "", werr
	}
	return "file:" + name, nil
}

// sniffIcon names the extension for picture content, or "" if it is not a
// picture magpie accepts.
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
	case len(b) >= 6 && bytes.HasPrefix(b, []byte{0, 0, 1, 0}) && (b[4] != 0 || b[5] != 0):
		// ICO: reserved 0, type 1, then a non-zero image count.
		return ".ico"
	case isSVG(b):
		return ".svg"
	}
	return ""
}

// isSVG accepts an XML document whose root is <svg>. An icon is drawn in the
// GUI, so anything live in it would run there: scripts, event handlers,
// javascript: links, and elements that embed other content are refused, as is
// a DOCTYPE, whose entities could hide any of them.
func isSVG(b []byte) bool {
	s := bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	s = bytes.TrimLeft(s, " \t\r\n")
	if !bytes.HasPrefix(s, []byte("<")) {
		return false
	}
	dec := xml.NewDecoder(bytes.NewReader(s))
	root := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.Directive:
			return false
		case xml.StartElement:
			if !root {
				root = true
				if !strings.EqualFold(t.Name.Local, "svg") {
					return false
				}
			}
			if unsafeSVGElement(t.Name.Local) {
				return false
			}
			for _, a := range t.Attr {
				if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") || unsafeSVGValue(a.Value) {
					return false
				}
			}
		}
	}
	return root
}

// unsafeSVGElement names the elements an icon may not contain.
func unsafeSVGElement(name string) bool {
	switch strings.ToLower(name) {
	case "script", "foreignobject", "iframe", "embed", "object":
		return true
	}
	return false
}

// unsafeSVGValue reports whether an attribute value is a script link. Spaces
// and control characters are dropped first, as a browser would ignore them.
func unsafeSVGValue(v string) bool {
	t := strings.ToLower(strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, v))
	return strings.Contains(t, "javascript:") || strings.Contains(t, "vbscript:")
}

// pruneIcons deletes stored pictures no provider refers to, once they are
// older than the grace period. Only files with magpie's own names are touched.
func pruneIcons(ps []Provider) {
	used := map[string]bool{}
	for _, p := range ps {
		if name, ok := strings.CutPrefix(p.Icon, "file:"); ok {
			used[name] = true
		}
	}
	dir := iconsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-iconGrace)
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || used[name] || IconFile(name) == "" {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
