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
func TestF09GroupRequestsAreNotCountedAsProviderUsage(t *testing.T) {
	f09RouteEnv(t)
	up := httptest.NewServer(&byKey{limited: map[string]bool{}})
	defer up.Close()
	f09KeysProvider(t, up.URL, "a", "", "", []string{"only-a"}, "ka")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := f09SendTo(t, s.Handler(), f09ReqBody("a/only-a", f09MsgUser("hi")), nil); code != 200 {
		t.Fatalf("provider model: %d %s", code, body)
	}
	if n := len(usage.Load(time.Time{})); n != 1 {
		t.Fatalf("provider model: %d usage records, want 1", n)
	}
	if code, body := f09SendTo(t, s.Handler(), f09ReqBody("group/mine", f09MsgUser("hi")), nil); code != 200 {
		t.Fatalf("group: %d %s", code, body)
	}
	if n := len(usage.Load(time.Time{})); n != 1 {
		t.Fatalf("group request counted in provider usage: %d records", n)
	}
}
