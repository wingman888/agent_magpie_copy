package provider

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStoreIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q", ref)
	}
	if again, _ := StoreIcon(png); again != ref {
		t.Errorf("same picture stored twice: %q, %q", ref, again)
	}
	p := IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(p); err != nil || string(b) != string(png) {
		t.Fatalf("IconFile(%q) = %q: %v", ref, p, err)
	}

	if ref, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg: %q, %v", ref, err)
	}
	if _, err := StoreIcon([]byte("hello")); err == nil {
		t.Error("text taken as a picture")
	}
	if _, err := StoreIcon(make([]byte, MaxIcon+1)); err == nil {
		t.Error("oversized picture taken")
	}
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", ""} {
		if IconFile(bad) != "" {
			t.Errorf("IconFile(%q) resolved", bad)
		}
	}
}

func TestFetchIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	mux := http.NewServeMux()
	mux.HandleFunc("/logo.png", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(png)
	})
	mux.HandleFunc("/huge.png", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(make([]byte, MaxIcon+1))
	})
	mux.HandleFunc("/text", func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("hello"))
	})
	mux.HandleFunc("/missing", func(rw http.ResponseWriter, r *http.Request) {
		http.NotFound(rw, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// httptest is plain http on loopback, which FetchIcon refuses on purpose;
	// the guard is checked here, then the download itself is driven directly.
	if _, err := FetchIcon(context.Background(), srv.URL+"/logo.png"); err == nil {
		t.Error("a loopback icon URL was fetched")
	}
	if _, err := iconURL(srv.URL + "/logo.png"); err == nil {
		t.Error("iconURL accepted a loopback host")
	}

	get := func(name string) (string, error) {
		return fetchIcon(context.Background(), srv.Client(), srv.URL+name)
	}
	ref, err := get("/logo.png")
	if err != nil || !strings.HasPrefix(ref, "file:") {
		t.Fatalf("fetch = %q, %v", ref, err)
	}
	if b, err := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:"))); err != nil || string(b) != string(png) {
		t.Fatalf("stored picture: %q, %v", b, err)
	}
	if _, err := get("/huge.png"); err == nil {
		t.Error("an oversized icon was stored")
	}
	if _, err := get("/text"); err == nil {
		t.Error("a non-picture was stored")
	}
	if _, err := get("/missing"); err == nil {
		t.Error("a 404 was stored")
	}
}

func TestPublicIP(t *testing.T) {
	public := []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"}
	private := []string{
		"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1",
		"0.0.0.0", "100.64.0.1", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "fc00::1", "fe80::1", "2002::1",
	}
	for _, s := range public {
		if !publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged private", s)
		}
	}
	for _, s := range private {
		if publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged public", s)
		}
	}
}

// The guarded client must refuse a host that resolves to loopback, even
// though the URL scheme and hostname look fine.
func TestGuardClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("x"))
	}))
	defer srv.Close()
	// a real FetchIcon already refuses 127.0.0.1 by hostname; this checks the
	// dial-time guard by aiming the same client at the loopback server
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := guardClient().Do(req); err == nil {
		t.Fatal("the guarded client reached a loopback address")
	}

	// a name that is not a literal address but resolves to loopback: the
	// guard has to judge the resolved IP, which is what stops DNS rebinding.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	req2, _ := http.NewRequest(http.MethodGet, "https://localhost:"+strconv.Itoa(port), nil)
	if _, err := guardClient().Do(req2); err == nil {
		t.Fatal("the guarded client reached a name resolving to loopback")
	}
}

// A public address must get past the guard: the dial may then fail (nothing
// is listening), but not because the guard refused it. A literal IP needs no
// resolver, so this is deterministic offline.
func TestPublicDialAllowsPublic(t *testing.T) {
	var got string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		got = addr
		return nil, errorf("connection refused for the test")
	}
	_, err := publicDial(context.Background(), dial, "tcp", "93.184.216.34:443")
	if got != "93.184.216.34:443" {
		t.Fatalf("public host not dialed: %q (%v)", got, err)
	}
	if err == nil || strings.Contains(err.Error(), "not public") {
		t.Fatalf("a public host was refused by the guard: %v", err)
	}

	// and the same shape for a private literal is refused before any dial
	got = ""
	if _, err := publicDial(context.Background(), dial, "tcp", "10.0.0.1:443"); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("a private address was not refused: %v", err)
	}
	if got != "" {
		t.Fatalf("dialed a private address anyway: %q", got)
	}
}
