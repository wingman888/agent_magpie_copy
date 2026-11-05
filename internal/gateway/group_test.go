package gateway

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group sends a request to its members in order; a member that
// is rate limited hands it to the next one.
func TestGroupTakesMembersInOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()

	b := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`}
	a := &fake{t: t, ctype: "application/json", reply: `{"id":"from-a","choices":[]}`}
	for _, x := range []struct {
		id string
		f  *fake
	}{{"b", b}, {"a", a}} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: x.id, Name: x.id, Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"b/m", "a/m"}}); err != nil {
		t.Fatal(err)
	}

	code, body := post(t, "/v1/chat/completions", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-a") {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(b.got, []byte(`"model":"m"`)) {
		t.Fatalf("b was not tried first: %s", b.got)
	}
}

// A group with no member ready answers 404 and says so.
func TestGroupWithNoReadyMemberIs404(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.SaveGroup(provider.Group{ID: "empty", Members: []string{"ghost/m"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"group/empty","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "group/empty has no model ready") {
		t.Fatalf("%d %s", code, body)
	}
}
