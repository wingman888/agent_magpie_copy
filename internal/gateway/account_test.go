package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

func TestCodexAccountNonStreamAndStream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "email": "a@b.com"})
	tok := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": tok, "account_id": "acc9"}})
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), auth, 0o600)

	var got []byte
	var head http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		head = r.Header
		// no Content-Type on purpose
		w.Header()["Content-Type"] = nil
		io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL
	defer func() { provider.CodexBase = old }()

	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	post := func(body string) (*http.Response, []byte) {
		res, err := http.Post(gw.URL+"/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	res, b := post(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":9,"temperature":0.5}`)
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil || v["object"] != "response" || !strings.Contains(string(b), "hel") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	var sent map[string]any
	json.Unmarshal(got, &sent)
	if sent["stream"] != true || sent["store"] != false || sent["model"] != "gpt-5.5" || sent["max_output_tokens"] != nil || sent["temperature"] != nil {
		t.Fatalf("%s", got)
	}
	if _, ok := sent["input"].([]any); !ok || head.Get("Authorization") != "Bearer "+tok || head.Get("chatgpt-account-id") != "acc9" {
		t.Fatalf("%s %v", got, head)
	}
	res, b = post(`{"model":"codex/gpt-5.5","input":"hi","stream":true,"temperature":0.5}`)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || !bytes.Contains(b, []byte("response.completed")) {
		t.Fatalf("%v %s", res.Header, b)
	}
	// signed out
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	res, b = post(`{"model":"codex/gpt-5.5","input":"hi"}`)
	if res.StatusCode != 404 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}
