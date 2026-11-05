package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
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

// FetchIcon downloads the picture an import link named and keeps it like
// any other, returning the Icon value that points at it. It runs when the
// user confirms the import, never at parse time; the URL is re-checked here
// so a crafted request can't point magpie at anything but a public https
// picture, and the read stops one byte past MaxIcon so a huge body is
// refused rather than buffered.
func FetchIcon(ctx context.Context, rawURL string) (string, error) {
	u, err := iconURL(rawURL)
	if err != nil {
		return "", err
	}
	return fetchIcon(ctx, guardClient(), u)
}

// guardClient is the http.Client icon fetches use: the default transport —
// so the proxy settings still apply — but with dialing wrapped in publicDial.
func guardClient() *http.Client {
	c := *http.DefaultClient
	t := http.DefaultTransport.(*http.Transport).Clone()
	dial := t.DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return publicDial(ctx, dial, network, addr)
	}
	c.Transport = t
	return &c
}

// publicDial resolves the host and refuses to connect when any address it
// maps to is not public. Checking the resolved address here, not just the
// URL's hostname, closes the door on a name that answers public once and
// private the next time (DNS rebinding): the same resolution this dial uses
// is the one that is judged.
func publicDial(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errorf("could not resolve %s", host)
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errorf("the icon host %s resolves to %s, which is not public", host, ip.IP)
		}
	}
	// Dial each resolved address by IP, so nothing resolves twice.
	var lastErr error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// publicIP is true for an address on the open internet: not loopback,
// private, link-local, unspecified, multicast or otherwise reserved.
func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64/10 carrier NAT, 192.0.0.0/24, 192.0.2/24, 198.18/15 and
		// friends: never a public web host.
		switch {
		case v4[0] == 100 && v4[1]&0xc0 == 64:
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0:
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2:
			return false
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
			return false
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100:
			return false
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113:
			return false
		}
		return true
	}
	// IPv6: unique local (fc00::/7) and 6to4/teredo tunnels are not public
	// web hosts either.
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return false
		}
		if ip[0] == 0x20 && ip[1] == 0x02 {
			return false // 2002::/16 6to4
		}
	}
	return true
}

// fetchIcon is the download itself, split out so tests can point it at a
// local server; the URL has already passed iconURL.
func fetchIcon(ctx context.Context, c *http.Client, u string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/*")
	res, err := c.Do(req)
	if err != nil {
		return "", errorf("could not fetch the icon: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errorf("the icon URL answered %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxIcon+1))
	if err != nil {
		return "", errorf("could not read the icon: %v", err)
	}
	return StoreIcon(b)
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
