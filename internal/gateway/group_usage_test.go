package gateway

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request to a routing group is not counted in the provider usage that
// the CLI shows; the same request to a provider's own model is.
func TestGroupRequestsAreNotCountedAsProviderUsage(t *testing.T) {
	routeEnv(t)
	up := httptest.NewServer(&byKey{limited: map[string]bool{}})
	defer up.Close()
	keysProvider(t, up.URL, "a", "", "", []string{"only-a"}, "ka")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := sendTo(t, s.Handler(), reqBody("a/only-a", msgUser("hi")), nil); code != 200 {
		t.Fatalf("provider model: %d %s", code, body)
	}
	if n := len(usage.Load(time.Time{})); n != 1 {
		t.Fatalf("provider model: %d usage records, want 1", n)
	}
	if code, body := sendTo(t, s.Handler(), reqBody("group/mine", msgUser("hi")), nil); code != 200 {
		t.Fatalf("group: %d %s", code, body)
	}
	if n := len(usage.Load(time.Time{})); n != 1 {
		t.Fatalf("group request counted in provider usage: %d records", n)
	}
}
