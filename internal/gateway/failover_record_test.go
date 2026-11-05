package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCallRecordsNamedKeyThatServed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &keyFake{limited: map[string]bool{"k1": true}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Models: []string{"m1"},
		Key: "k1", Keys: []provider.KeyEntry{{Name: "team", Key: "k2"}}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	c := srv.Recent()[0]
	if c.Key != "team" {
		t.Fatalf("call Key = %q, want the served key's name %q", c.Key, "team")
	}
	if !strings.Contains(c.Fallback, "fake") {
		t.Fatalf("call Fallback = %q, want it to name the failed first key's provider", c.Fallback)
	}
}

func TestCallRecordsFingerprintForUnnamedKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &keyFake{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Models: []string{"m1"}, Key: "k9"}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if c := srv.Recent()[0]; c.Key != provider.KeyRef("k9") {
		t.Fatalf("call Key = %q, want fingerprint %q", c.Key, provider.KeyRef("k9"))
	}
}

func TestFailoverStatus403(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"exceeded max tokens", false},
		{"the request limit was exceeded for this field", false},
		{"quota exceeded", true},
		{"额度已用完", true},
		{"insufficient balance", true},
		{"forbidden", false},
	}
	for _, c := range cases {
		if got := failoverStatus(403, []byte(c.body)); got != c.want {
			t.Errorf("failoverStatus(403, %q) = %v, want %v", c.body, got, c.want)
		}
	}
}
