package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An upstream that echoes the key it was called with must not leak it to the
// caller, the call record or the log: the reply carries only the mask.
func TestMultiAcctEchoedKeyNeverReachesCaller(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 401,
		reply: `{"error":{"message":"invalid api key ` + multiAcctPersonalKey + `"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: personal}, spare, []provider.KeySlot{{Name: "个人", Key: multiAcctPersonalKey}})
	s := New()
	code, body := multiAcctSendChat(t, s)
	if code != 401 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, multiAcctPersonalKey) || !strings.Contains(body, provider.Mask(multiAcctPersonalKey)) {
		t.Fatalf("client saw the key or no mask: %s", body)
	}
	c := s.Recent()[0]
	for name, field := range map[string]string{"Error": c.Error, "Fallback": c.Fallback, "ResponseBody": c.ResponseBody} {
		if strings.Contains(field, multiAcctPersonalKey) {
			t.Fatalf("full key in call record %s: %q", name, field)
		}
	}
	if !strings.Contains(c.Error, provider.Mask(multiAcctPersonalKey)) {
		t.Fatalf("record did not show the masked key: %q", c.Error)
	}
	if spare.got != nil {
		t.Fatal("a 401 without quota words should not fail over")
	}
}

// A quota failure that echoes its key is skipped, and the skip is recorded
// without the key while the next key answers cleanly.
func TestMultiAcctEchoedKeyInFailoverIsRedacted(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429,
		reply: `{"error":{"message":"rate limited for ` + multiAcctPersonalKey + `"}}`}
	team := &fake{t: t, ctype: "application/json", reply: `{"id":"from-team","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team}, spare, []provider.KeySlot{
		{Name: "个人", Key: multiAcctPersonalKey}, {Name: "团队", Key: multiAcctTeamKey}})
	s := New()
	code, body := multiAcctSendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, multiAcctPersonalKey) {
		t.Fatalf("client saw the key: %s", body)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, provider.Mask(multiAcctPersonalKey)) {
		t.Fatalf("recorded %q", c.Fallback)
	}
	for name, field := range map[string]string{"Fallback": c.Fallback, "Error": c.Error, "ResponseBody": c.ResponseBody} {
		if strings.Contains(field, multiAcctPersonalKey) {
			t.Fatalf("full key in call record %s: %q", name, field)
		}
	}
}

// With every key of the primary resting, its keys are still spent in order
// before the fallback: [personal, team, spare].
func TestMultiAcctAllKeysRestingStayBeforeFallback(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	team := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	kr := multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team}
	var order []string
	plan := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		kr.ServeHTTP(w, r)
	})
	multiAcctSetupKeys(t, plan, spare, []provider.KeySlot{{Name: "个人", Key: multiAcctPersonalKey}, {Name: "团队", Key: multiAcctTeamKey}})
	restingUntil.Lock()
	restingUntil.m["plan/"+provider.KeyRef(multiAcctPersonalKey)] = time.Now().Add(time.Minute)
	restingUntil.m["plan/"+provider.KeyRef(multiAcctTeamKey)] = time.Now().Add(time.Minute)
	restingUntil.Unlock()

	s := New()
	if code, body := multiAcctSendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	want := []string{multiAcctPersonalKey, multiAcctTeamKey}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("primary keys tried in order %v, want %v", order, want)
	}
	if spare.got == nil {
		t.Fatal("fallback was not reached after the primary's keys")
	}
	if c := s.Recent()[0]; c.Provider != "spare" {
		t.Fatalf("answered by %s", c.Provider)
	}
}
