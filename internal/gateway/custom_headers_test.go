package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestChForwardSendsCustomHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	p := provider.Provider{
		ID:      "chfwd",
		Chat:    srv.URL + "/v1",
		Key:     "sk-real",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer mine"},
	}
	s := New()
	res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if v := got.Get("X-Org-Id"); v != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", v)
	}
	if v := got.Values("Authorization"); len(v) != 1 || v[0] != "Bearer mine" {
		t.Errorf("Authorization = %v, want the user's value only", v)
	}
}
