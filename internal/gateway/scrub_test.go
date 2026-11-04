package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A single-key provider whose upstream echoes the full key in a 429 body: the
// call record must hold the masked key only, and the request still falls back.
func TestSingleKeyErrorEchoIsScrubbed(t *testing.T) {
	const solo = "sk-solo-0000000000003"
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()

	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"key `+solo+` rate limited"}}`)
	})
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	spareUp := httptest.NewServer(spare)
	t.Cleanup(spareUp.Close)

	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k", Chat: spareUp.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: solo,
		Chat: up.URL + "/v1", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if strings.Contains(c.Fallback, solo) || strings.Contains(c.Error, solo) {
		t.Fatalf("full key in record: %+v", c)
	}
	if !strings.Contains(c.Fallback, "plan: ") || !strings.Contains(c.Fallback, provider.Mask(solo)) {
		t.Fatalf("recorded %q, want the masked key under plan", c.Fallback)
	}
}
