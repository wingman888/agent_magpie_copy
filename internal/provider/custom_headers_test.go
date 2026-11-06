package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// chRawServer answers every request with a fixed JSON body and keeps the
// request's header block byte for byte, so the tests can see the names as
// they went over the wire (net/http would canonicalise them on the way in).
type chRawServer struct {
	ln    net.Listener
	mu    sync.Mutex
	heads []string
}

func chStartRawServer(t *testing.T, body string) *chRawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &chRawServer{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, body)
		}
	}()
	return s
}

func (s *chRawServer) URL() string { return "http://" + s.ln.Addr().String() }

// Heads returns the raw header block of every request seen so far.
func (s *chRawServer) Heads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.heads...)
}

func (s *chRawServer) serve(conn net.Conn, body string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	var raw strings.Builder
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		raw.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if length > 0 {
		io.CopyN(io.Discard, r, int64(length))
	}
	s.mu.Lock()
	s.heads = append(s.heads, raw.String())
	s.mu.Unlock()
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

// chHeaderLines returns the header lines whose name matches case-insensitively,
// exactly as sent (name and value, without the line ending).
func chHeaderLines(raw, name string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\r\n") {
		if n, _, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(n), name) {
			out = append(out, line)
		}
	}
	return out
}

// chIsolate points config and cache at a temp dir so nothing touches the
// real machine.
func chIsolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestChCustomHeadersOnTheWire(t *testing.T) {
	srv := chStartRawServer(t, `{"data":[{"id":"m1"}]}`)
	p := Provider{
		ID:    "chrelay",
		Name:  "Relay",
		Chat:  srv.URL() + "/v1",
		Key:   "sk-real",
		Headers: map[string]string{
			"X-ORG-id":      "acme",
			"authorization": "Bearer custom",
			"X-Api-Token":   "t-1",
		},
	}
	res := probe(context.Background(), p, Chat, p.Chat+"/chat/completions", []byte(`{"model":"m1"}`), "m1")
	if !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	heads := srv.Heads()
	if len(heads) != 1 {
		t.Fatalf("want 1 request, got %d", len(heads))
	}
	raw := heads[0]
	if !strings.Contains(raw, "\r\nX-ORG-id: acme\r\n") {
		t.Errorf("X-ORG-id not sent with its case:\n%s", raw)
	}
	if strings.Contains(raw, "X-Org-Id:") {
		t.Errorf("header name was canonicalised:\n%s", raw)
	}
	if !strings.Contains(raw, "\r\nX-Api-Token: t-1\r\n") {
		t.Errorf("X-Api-Token missing:\n%s", raw)
	}
	auth := chHeaderLines(raw, "Authorization")
	if len(auth) != 1 || auth[0] != "authorization: Bearer custom" {
		t.Errorf("want exactly the user's authorization header, got %q", auth)
	}
}

func TestChCustomHeadersOnModelList(t *testing.T) {
	chIsolate(t)
	srv := chStartRawServer(t, `{"data":[{"id":"m1"}]}`)
	p := Provider{
		ID:      "chrelay",
		Chat:    srv.URL() + "/v1",
		Key:     "sk-real",
		Headers: map[string]string{"X-ORG-id": "acme"},
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	heads := srv.Heads()
	if len(heads) == 0 {
		t.Fatal("no model list request reached the server")
	}
	for _, raw := range heads {
		if !strings.Contains(raw, "\r\nX-ORG-id: acme\r\n") {
			t.Errorf("model list request missing header:\n%s", raw)
		}
	}
}

func TestChCustomHeadersSkippedForPresetAndAccount(t *testing.T) {
	hs := map[string]string{"X-Org-Id": "acme"}
	for name, p := range map[string]Provider{
		"preset":  {ID: "ds", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk", Headers: hs},
		"account": {ID: "codex", Account: &Account{Agent: "codex", User: "u"}, Responses: "https://x.example/v1", Headers: hs},
	} {
		req, _ := http.NewRequest(http.MethodPost, "https://x.example/v1/chat/completions", nil)
		if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
			t.Fatal(err)
		}
		if v := req.Header.Values("X-Org-Id"); len(v) != 0 {
			t.Errorf("%s sent custom header: %v", name, v)
		}
		if _, ok := req.Header["X-ORG-id"]; ok {
			t.Errorf("%s sent custom header under its raw name", name)
		}
	}
}

func TestChCustomHeadersSignOverridesAuth(t *testing.T) {
	p := Provider{ID: "chrelay", Chat: "https://relay.example/v1", Key: "sk-real",
		Headers: map[string]string{"Authorization": "Bearer mine", "x-org-id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["Authorization"]; len(got) != 1 || got[0] != "Bearer mine" {
		t.Errorf("Authorization = %v, want the user's value only", got)
	}
	if got := req.Header["x-org-id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("x-org-id = %v", got)
	}
}

func TestChSaveKeepsHeadersOnlyForCustom(t *testing.T) {
	chIsolate(t)
	hs := map[string]string{"  X-A  ": "  v  ", "": "dropped", "   ": "dropped too", "X-ORG-id": "b"}

	if err := Save(Provider{Name: "Chrelay", Chat: "https://relay.example/v1", Key: "k", Headers: hs}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "Chds", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "k", Headers: hs}); err != nil {
		t.Fatal(err)
	}
	stored := map[string]Provider{}
	for _, p := range load().Providers {
		stored[p.ID] = p
	}
	got := stored["chrelay"].Headers
	if len(got) != 2 || got["X-A"] != "v" || got["X-ORG-id"] != "b" {
		t.Errorf("custom headers stored as %v", got)
	}
	if h := stored["chds"].Headers; h != nil {
		t.Errorf("preset kept headers: %v", h)
	}

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"headers"`) != 1 {
		t.Errorf("headers written for preset or empty map:\n%s", b)
	}
}

func TestChEmptyHeadersNotWritten(t *testing.T) {
	chIsolate(t)
	if err := Save(Provider{Name: "Chplain", Chat: "https://relay.example/v1", Key: "k",
		Headers: map[string]string{"  ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "headers") {
		t.Errorf("empty headers were written:\n%s", b)
	}
}
