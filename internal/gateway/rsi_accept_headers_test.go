package gateway_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptGatewayForwardsCustomHeaders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var lines []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				r.ReadString('\n')
				cl := 0
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					l = strings.TrimRight(l, "\r\n")
					if l == "" {
						break
					}
					mu.Lock()
					lines = append(lines, l)
					mu.Unlock()
					if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "content-length") {
						for _, ch := range strings.TrimSpace(v) {
							cl = cl*10 + int(ch-'0')
						}
					}
				}
				for i := 0; i < cl; i++ {
					r.ReadByte()
				}
				body := `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body))
			}()
		}
	}()

	p := filepath.Join(home, ".config", "magpie", "providers.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(map[string]any{"providers": []map[string]any{{
		"id": "fake", "name": "Fake", "key": "sk-k", "chat": "http://" + ln.Addr().String() + "/v1", "models": []string{"m1"},
		"headers": map[string]string{"X-ORG-id": "acme", "Authorization": "Token mine"},
	}}})
	os.WriteFile(p, b, 0o600)
	if _, err := provider.Find("fake"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/v1/chat/completions"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`))
		gateway.New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	has := func(s string) bool {
		for _, l := range lines {
			if l == s {
				return true
			}
		}
		return false
	}
	if !has("X-ORG-id: acme") {
		t.Errorf("custom header (exact case) missing: %q", lines)
	}
	if !has("Authorization: Token mine") || has("Authorization: Bearer sk-k") {
		t.Errorf("authorization not overridden: %q", lines)
	}
}

func itoa(n int) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
