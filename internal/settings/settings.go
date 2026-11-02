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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// Proxy is the proxy for requests magpie sends to vendors: "" follows the
	// environment and then the OS, "direct" uses none, anything else is a
	// proxy address such as http://127.0.0.1:7890 or socks5://127.0.0.1:1080.
	Proxy string `json:"proxy,omitempty"`
}

// ProxyDirect is the Proxy value that turns proxying off.
const ProxyDirect = "direct"

// IsDirect reports whether a Proxy value means "no proxy".
func IsDirect(proxy string) bool {
	return strings.EqualFold(strings.TrimSpace(proxy), ProxyDirect)
}

// ParseProxy parses a proxy address. http://, https://, socks5:// and
// socks5h:// are accepted; a bare host:port is an HTTP proxy.
func ParseProxy(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	bad := func(why string) error {
		return fmt.Errorf("invalid proxy %q: %s; use e.g. http://127.0.0.1:7890 or socks5://127.0.0.1:1080, or %q for none", s, why, ProxyDirect)
	}
	if s == "" {
		return nil, bad("empty")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return nil, bad("contains whitespace")
	}
	raw := s
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, bad("cannot be parsed")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, bad(fmt.Sprintf("unsupported scheme %q", u.Scheme))
	}
	host := u.Hostname()
	if host == "" {
		return nil, bad("missing host")
	}
	if strings.HasPrefix(u.Host, "[") {
		if net.ParseIP(host) == nil {
			return nil, bad("bad IPv6 address")
		}
	} else {
		for _, c := range host {
			ok := c == '-' || c == '.' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c > 127
			if !ok {
				return nil, bad("bad host")
			}
		}
	}
	if p := u.Port(); p != "" {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil, bad("bad port")
			}
			n = n*10 + int(c-'0')
			if n > 65535 {
				return nil, bad("bad port")
			}
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, bad("missing port")
	}
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("unexpected path")
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

func checkProxy(p string) error {
	if p == "" || IsDirect(p) {
		return nil
	}
	_, err := ParseProxy(p)
	return err
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
	if err := checkProxy(s.Proxy); err != nil {
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

func (s Settings) normal() Settings {
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.Lang == "" {
		s.Lang = "system"
	}
	if s.Tray == "" {
		s.Tray = "panel"
	}
	s.Proxy = strings.TrimSpace(s.Proxy)
	if IsDirect(s.Proxy) {
		s.Proxy = ProxyDirect
	}
	return s
}
