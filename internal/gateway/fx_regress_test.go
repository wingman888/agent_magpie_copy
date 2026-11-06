package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// fxNoCTChat is an ordinary Chat upstream that streams its reply but sends
// no Content-Type at all.
func fxNoCTChat(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil // suppress sniffing: no header at all
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fxplain", Name: "FxPlain", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

// An ordinary provider that sends no Content-Type is not sniffed: the reply
// is refused as it always was, not read as an event stream.
func TestFxPlainProviderNoContentTypeIsStillRefused(t *testing.T) {
	fxNoCTChat(t)
	code, body := post(t, "/v1/responses", `{"model":"m1","stream":true,"input":"hi"}`)
	if code != http.StatusBadGateway || !strings.Contains(body, "did not stream") {
		t.Fatalf("status %d, body %s; want 502 did not stream", code, body)
	}
}

// Account providers expose all of their models, however many there are.
func TestFxAccountExposesEveryModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex", User: "fx"}}
	if len(p.Available()) == 0 {
		t.Fatal("no models for the codex login")
	}
	if got, want := len(p.Exposed()), len(p.Available()); got != want {
		t.Errorf("exposed %d of %d", got, want)
	}
}
