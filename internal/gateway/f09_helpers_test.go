package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// f09RouteEnv starts each test with no config, no rests and no conversation.
func f09RouteEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f09ExpireRests()
	affState.Lock()
	affState.last = map[string]affAnswer{}
	affState.Unlock()
	routed.Lock()
	routed.rotate = map[string]rotation{}
	routed.Unlock()
}

// f09ExpireRests makes every candidate ready again, as a minute later would.
func f09ExpireRests() {
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// f09RestFor makes a candidate rest, as a failure would.
func f09RestFor(id string) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(time.Minute)
	restingUntil.note[id] = Rest{Why: failRate, Status: 429, By: "cooldown"}
	restingUntil.Unlock()
}

// f09KeysProvider saves a provider whose first key is keys[0], then the rest.
func f09KeysProvider(t *testing.T, url, id, affinity, routing string, models []string, keys ...string) {
	t.Helper()
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Chat: url + "/v1", Models: models,
		Affinity: affinity, Routing: routing, Key: keys[0], KeyName: "Key1"}
	for i, k := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Name: fmt.Sprintf("Key%d", i+2), Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// f09KeyRest is the candidate id of a provider's key.
func f09KeyRest(id, key string) string { return id + "#" + provider.KeyID(key) }

// f09SendTo posts a chat request to a gateway handler.
func f09SendTo(t *testing.T, h http.Handler, body string, hdr map[string]string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// f09LastRoute is the trace of the latest request.
func f09LastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no request traced")
	}
	return rs[len(rs)-1]
}

// Messages of a conversation, as chat clients send them.
func f09MsgUser(text string) string {
	return `{"role":"user","content":"` + text + `"}`
}

func f09MsgCall(id string) string {
	return `{"role":"assistant","content":null,"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"f","arguments":"{}"}}]}`
}

func f09MsgResult(id string) string {
	return `{"role":"tool","tool_call_id":"` + id + `","content":"ok"}`
}

func f09ReqBody(model string, msgs ...string) string {
	return `{"model":"` + model + `","messages":[` + strings.Join(msgs, ",") + `]}`
}
