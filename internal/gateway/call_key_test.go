package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The recent call names the key that answered, and the failed key on the way.
func TestCallRecordsTheKeyThatAnswered(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 429
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.RenameKey("relay", provider.KeyRef("k1"), "personal"))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))

	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", chatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	c := srv.Recent()[0]
	if c.Key != "team" {
		t.Fatalf("call key = %q, want the key that answered (team)", c.Key)
	}
	if c.Fallback != "relay (personal)" {
		t.Fatalf("call fallback = %q, want the failed key named", c.Fallback)
	}
	if c.Provider != "relay" {
		t.Fatalf("call provider = %q, want relay", c.Provider)
	}

	// the next request goes straight to the working key, with nothing failed
	code, body = callOn(srv, "/v1/chat/completions", chatBody)
	if code != 200 {
		t.Fatalf("second status %d: %s", code, body)
	}
	c = srv.Recent()[0]
	if c.Key != "team" || c.Fallback != "" {
		t.Fatalf("second call key=%q fallback=%q, want team and no fallback", c.Key, c.Fallback)
	}
	if f.count("k1") != 1 {
		t.Fatalf("k1 tried %d times, want 1 (the second request must skip it)", f.count("k1"))
	}
}
