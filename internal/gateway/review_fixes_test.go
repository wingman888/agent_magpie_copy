package gateway

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCallRecordsTheKeyThatAnswered(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	s := New()
	if code, body := sendChat(s); code != 200 {
		t.Fatalf("request: %d %s", code, body)
	}
	if got := s.Recent()[0].Key; got != "team" {
		t.Fatalf("call key = %q, want the second key's name %q", got, "team")
	}
}

func TestCallKeyIsEmptyWhenTheRequestFails(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests, "sk-2": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	s := New()
	if code, _ := sendChat(s); code != 429 {
		t.Fatalf("status %d, want 429", code)
	}
	if got := s.Recent()[0].Key; got != "" {
		t.Fatalf("call key = %q on a failed request, want empty", got)
	}
}

func TestCallKeyFallsBackToFingerprintWhenUnnamed(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// the second key has no name; it is recorded by its fingerprint
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "sk-1", KeyName: "personal",
		Chat: srv.URL + "/v1", Models: []string{"m1"}, Keys: []provider.ExtraKey{{Key: "sk-2"}}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, _ := sendChat(s); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got, want := s.Recent()[0].Key, provider.KeyRef("sk-2"); got != want {
		t.Fatalf("call key = %q, want fingerprint %q", got, want)
	}
}

func TestCoolingKeysAreTriedWhenAllAreCooling(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests, "sk-2": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	s := New()
	sendChat(s)
	sendChat(s)
	// both keys failed within the cooldown: the next request keeps the order
	// and tries them both again, first key first
	if want := []string{"sk-1", "sk-2", "sk-1", "sk-2"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
}

func TestCoolingKeyIsLeftOutWhileAnotherIsReady(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	s := New()
	sendChat(s)
	sendChat(s)
	// sk-1 failed a moment ago and sk-2 is ready: sk-1 is not tried at all
	if want := []string{"sk-1", "sk-2", "sk-2"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
}

func TestOrdinaryForbiddenIsNotRetriedWithAnotherKey(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusForbidden, "sk-2": http.StatusOK}}
	keyedRelay(t, up)
	code, body := sendChat(New())
	if code != 403 || !strings.Contains(body, "sk-1") {
		t.Fatalf("%d %s; want the first key's plain 403", code, body)
	}
	if want := []string{"sk-1"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
}

func TestKeyRetryableForbidden(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"error":"access limit reached for this path"}`, false},
		{`{"error":"request exceeds the allowed size"}`, false},
		{`{"error":"forbidden"}`, false},
		{`{"error":"quota exceeded for this key"}`, true},
		{`{"error":"insufficient balance"}`, true},
		{`{"error":"credit exhausted"}`, true},
		{`{"error":"额度已用完"}`, true},
		{`{"error":"余额不足"}`, true},
	} {
		if got := keyRetryable(http.StatusForbidden, tc.body); got != tc.want {
			t.Errorf("keyRetryable(403, %s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
