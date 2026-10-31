// Package settings keeps the few preferences the desktop app has: which
// palette to paint with and which language to speak. Everything else magpie
// knows is derived from the agents' own files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for both.
package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// Proxy is the proxy for requests to model vendors: "" follows the
	// environment and then the OS, "direct" uses none, anything else is a
	// proxy address (http, https, socks5, socks5h; bare host:port means http).
	Proxy string `json:"proxy,omitempty"`
}

// Themes and Langs are the accepted values, in the order the UI offers them.
var (
	Themes = []string{"system", "light", "dark"}
	Langs  = []string{"system", "en", "zh"}
	Trays  = []string{"panel", "window"}
)

// Path is the settings file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "settings.json")
}

// Dir is the folder every magpie file lives in.
func Dir() string { return filepath.Dir(Path()) }

// Load reads the settings; anything missing or unreadable is the default.
func Load() Settings {
	var s Settings
	if b, err := os.ReadFile(Path()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.normal()
}

// Save validates and writes the settings.
func Save(s Settings) error {
	s = s.normal()
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
	}
	if _, _, err := ParseProxy(s.Proxy); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(b, '\n'), 0o644)
}

// ProxyExample is the hint shown with proxy errors.
const ProxyExample = "http://127.0.0.1:7890"

// ParseProxy interprets a proxy setting. Empty gives (nil, false, nil),
// "direct" (any case) gives direct=true, anything else must be a proxy
// address with scheme http, https, socks5 or socks5h; without a scheme it is
// an http proxy.
func ParseProxy(raw string) (u *url.URL, direct bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}
	if strings.EqualFold(raw, "direct") {
		return nil, true, nil
	}
	bad := func(why string) error {
		return fmt.Errorf("invalid proxy %q: %s; use e.g. %s or socks5://127.0.0.1:1080, or \"direct\" for no proxy", raw, why, ProxyExample)
	}
	full := raw
	if !strings.Contains(raw, "://") {
		full = "http://" + raw
	}
	u, err = url.Parse(full)
	if err != nil {
		return nil, false, bad("cannot parse")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, false, bad("unsupported scheme " + strconv.Quote(u.Scheme))
	}
	if u.Hostname() == "" {
		return nil, false, bad("missing host")
	}
	if p := u.Port(); p != "" {
		if n, e := strconv.Atoi(p); e != nil || n < 1 || n > 65535 {
			return nil, false, bad("bad port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, false, bad("bad port")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, false, bad("unexpected path")
	}
	u.Path = ""
	return u, false, nil
}

func (s Settings) normal() Settings {
	s.Proxy = strings.TrimSpace(s.Proxy)
	if strings.EqualFold(s.Proxy, "direct") {
		s.Proxy = "direct"
	}
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.Lang == "" {
		s.Lang = "system"
	}
	if s.Tray == "" {
		s.Tray = "panel"
	}
	return s
}
