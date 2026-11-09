package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// kfoUpstream stands in for one vendor that takes several keys. Each key
// gets the status in codes (200 when absent), and the keys it was called
// with are recorded in seen, in arrival order.
type kfoUpstream struct {
	mu    sync.Mutex
	codes map[string]int
	seen  []string
}

func (u *kfoUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen = append(u.seen, key)
	code := u.codes[key]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code >= 400 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

const (
	kfoFront = "sk-personal-0001"
	kfoTeam  = "sk-team-00002"
	kfoSpare = "sk-spare-000004"
	kfoReq   = `{"model":"kfoplan/m1","messages":[{"role":"user","content":"hi"}]}`
)

// kfoPlan is a provider whose front key is named 个人, with the given keys
// after it. It falls back to kfospare/m2.
func kfoPlan(keys ...provider.ExtraKey) provider.Provider {
	return provider.Provider{ID: "kfoplan", Name: "KFO Plan", Key: kfoFront, KeyName: "个人",
		Keys: keys, Models: []string{"m1"}, Fallback: []string{"kfospare/m2"}}
}

// kfoSetup saves the plan provider and its spare, each served by its upstream.
func kfoSetup(t *testing.T, plan provider.Provider, planUp, spareUp *kfoUpstream) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	spare := provider.Provider{ID: "kfospare", Name: "KFO Spare", Key: kfoSpare, Models: []string{"m2"}}
	for _, x := range []struct {
		p provider.Provider
		u *kfoUpstream
	}{{plan, planUp}, {spare, spareUp}} {
		up := httptest.NewServer(x.u)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

func kfoSend(t *testing.T, s *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(kfoReq)))
	return rec.Code, rec.Body.String()
}

func TestKfoFailsOverToTheNextKey(t *testing.T) {
	plan := &kfoUpstream{codes: map[string]int{kfoFront: 429}}
	kfoSetup(t, kfoPlan(provider.ExtraKey{Name: "团队", Key: kfoTeam}), plan, &kfoUpstream{})
	s := New()
	code, body := kfoSend(t, s)
	if code != 200 || !strings.Contains(body, "from-"+kfoTeam) || strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(plan.seen, ","); got != kfoFront+","+kfoTeam {
		t.Fatalf("keys tried: %s", got)
	}
	c := s.Recent()[0]
	if c.Provider != "kfoplan" || !strings.Contains(c.Fallback, "kfoplan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
}

func TestKfoRestsTheFailedKeyOnly(t *testing.T) {
	plan := &kfoUpstream{codes: map[string]int{kfoFront: 429}}
	kfoSetup(t, kfoPlan(provider.ExtraKey{Name: "团队", Key: kfoTeam}), plan, &kfoUpstream{})
	s := New()
	kfoSend(t, s)
	plan.seen = nil
	if code, body := kfoSend(t, s); code != 200 || !strings.Contains(body, "from-"+kfoTeam) {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(plan.seen, ","); got != kfoTeam {
		t.Fatalf("a resting key was tried first: %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("nothing should have been skipped the second time: %q", c.Fallback)
	}
}

func TestKfoDisabledKeyIsNeverSent(t *testing.T) {
	plan := &kfoUpstream{codes: map[string]int{kfoFront: 429}}
	spare := &kfoUpstream{}
	kfoSetup(t, kfoPlan(provider.ExtraKey{Name: "团队", Key: kfoTeam, Off: true}), plan, spare)
	s := New()
	if code, body := kfoSend(t, s); code != 200 || !strings.Contains(body, "from-"+kfoSpare) {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(plan.seen, ","); got != kfoFront {
		t.Fatalf("keys tried on the plan: %s", got)
	}
	// one enabled key: the record keeps its old form
	if c := s.Recent()[0]; !strings.HasPrefix(c.Fallback, "kfoplan: ") {
		t.Fatalf("recorded %q", c.Fallback)
	}
}

func TestKfoFallsBackOnlyWhenEveryKeyFails(t *testing.T) {
	plan := &kfoUpstream{codes: map[string]int{kfoFront: 429, kfoTeam: 429}}
	spare := &kfoUpstream{}
	kfoSetup(t, kfoPlan(provider.ExtraKey{Key: kfoTeam}), plan, spare)
	s := New()
	code, body := kfoSend(t, s)
	if code != 200 || !strings.Contains(body, "from-"+kfoSpare) {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(plan.seen, ","); got != kfoFront+","+kfoTeam {
		t.Fatalf("keys tried on the plan: %s", got)
	}
	if got := strings.Join(spare.seen, ","); got != kfoSpare {
		t.Fatalf("spare got: %s", got)
	}
	c := s.Recent()[0]
	if c.Provider != "kfospare" {
		t.Fatalf("recorded %+v", c)
	}
	// the unnamed key is shown by its masked form, never in full
	if !strings.Contains(c.Fallback, "kfoplan(个人): ") || !strings.Contains(c.Fallback, "kfoplan("+provider.Mask(kfoTeam)+"): ") {
		t.Fatalf("fallback record %q", c.Fallback)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{kfoFront, kfoTeam} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("full key %s in the call record", k)
		}
	}
}
