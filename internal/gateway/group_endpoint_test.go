package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group's model object describes its first member that can be used now,
// under the id the client asked for.
func TestF09GroupModelAnswersAsItsFirstMember(t *testing.T) {
	f09KeyPoolSetup(t)
	for _, p := range []provider.Provider{
		{ID: "gepb", Name: "EB", Key: "kb", Chat: "http://b.test/v1", Models: []string{"vendor/m"}},
		{ID: "gepa", Name: "EA", Key: "ka", Chat: "http://a.test/v1", Models: []string{"only-a"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "eget", Name: "Eget", Routing: provider.Ordered,
		Members: []provider.Member{{Provider: "gepb", Model: "vendor/m"}, {Provider: "gepa", Model: "only-a"}}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/group%2Feget", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "group/eget" || got["owned_by"] != "gepb" {
		t.Fatalf("answered as %v, want group/eget owned by its first member gepb", got)
	}

	// the listing names the group as the magpie's own, not blank
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"group/eget"`) || !strings.Contains(rec.Body.String(), `"owned_by":"magpie"`) {
		t.Fatalf("listing: %s", rec.Body.String())
	}
}

// A group with no member ready is a 404 that says in Chinese and English
// that it has no model ready, both when asked about and when called.
func TestF09GroupWithNoMemberIs404(t *testing.T) {
	f09KeyPoolSetup(t)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/group%2Fnobody", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "没有模型就绪") {
		t.Fatalf("model: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/nobody","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "没有模型就绪") || !strings.Contains(rec.Body.String(), "has no model ready") {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
}
