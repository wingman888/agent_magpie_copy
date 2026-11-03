package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyed is an upstream that answers by the key it is sent: a key in codes
// gets that status with a rate-limit body, any other key gets reply.
type keyed struct {
	mu    sync.Mutex
	codes map[string]int
	reply string
	hits  []string // keys seen, in order
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.hits = append(k.hits, key)
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code, ok := k.codes[key]; ok {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	}
	io.WriteString(w, k.reply)
}

func (k *keyed) sawKey(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, h := range k.hits {
		if h == key {
			return true
		}
	}
	return false
}

func (k *keyed) reset() {
	k.mu.Lock()
	k.hits = nil
	k.mu.Unlock()
}

// keyedSetup saves "plan" with two keys ("个人" first, then "团队") on upstream
// plan, and "spare" as its fallback on upstream spare.
func keyedSetup(t *testing.T, plan, spare *keyed) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()

	up := httptest.NewServer(plan)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "sk-personal-0001", KeyName: "个人",
		Chat: up.URL + "/v1", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "团队", "sk-team-00002"); err != nil {
		t.Fatal(err)
	}

	sup := httptest.NewServer(spare)
	t.Cleanup(sup.Close)
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "sk-spare-spare", Chat: sup.URL + "/v1",
		Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
}

// keyRefAt is the id of the plan provider's key at position i in attempt order.
func keyRefAt(t *testing.T, i int) string {
	t.Helper()
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	list := p.KeyList()
	if i >= len(list) {
		t.Fatalf("plan has %d keys, want index %d", len(list), i)
	}
	return list[i].ID
}

func sendChat(s *Server) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

const (
	personalKey = "sk-personal-0001"
	teamKey     = "sk-team-00002"
)

func TestSecondKeyAnswersWhenFirstIsRateLimited(t *testing.T) {
	plan := &keyed{codes: map[string]int{personalKey: 429}, reply: `{"id":"from-plan","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)

	s := New()
	code, body := sendChat(s)
	if code != 200 || !strings.Contains(body, "from-plan") || strings.Contains(body, "slow down") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{personalKey, teamKey}; !equalStrings(plan.hits, want) {
		t.Fatalf("plan saw keys %v, want %v", plan.hits, want)
	}
	if spare.hits != nil {
		t.Fatal("the fallback was used although a key still worked")
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || !strings.HasPrefix(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "slow down") {
		t.Fatalf("recorded %+v", c)
	}
	if strings.Contains(c.Fallback, personalKey) || strings.Contains(c.Fallback, teamKey) {
		t.Fatalf("a full key reached the call record: %q", c.Fallback)
	}
}

func TestRestingKeyIsNotTriedAgainWithinAMinute(t *testing.T) {
	plan := &keyed{codes: map[string]int{personalKey: 429}, reply: `{"id":"from-plan","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)

	s := New()
	sendChat(s)
	plan.reset()
	code, body := sendChat(s)
	if code != 200 || !strings.Contains(body, "from-plan") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{teamKey}; !equalStrings(plan.hits, want) {
		t.Fatalf("second request saw keys %v, want only the team key %v", plan.hits, want)
	}
}

func TestOffKeyIsNeverSent(t *testing.T) {
	plan := &keyed{codes: map[string]int{personalKey: 429}, reply: `{"id":"from-plan","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)
	if err := provider.SetKeyOn("plan", keyRefAt(t, 1), false); err != nil {
		t.Fatal(err)
	}

	s := New()
	code, body := sendChat(s)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.sawKey(teamKey) {
		t.Fatal("a key that is off was sent upstream")
	}
	if want := []string{personalKey}; !equalStrings(plan.hits, want) {
		t.Fatalf("plan saw keys %v, want %v", plan.hits, want)
	}
	// only one key is on, so the record keeps the single-key format
	if c := s.Recent()[0]; !strings.HasPrefix(c.Fallback, "plan: ") {
		t.Fatalf("recorded %+v", c)
	}
}

func TestFallbackWhenEveryKeyFails(t *testing.T) {
	const unnamed = "sk-unnamed-0003"
	plan := &keyed{codes: map[string]int{personalKey: 429, teamKey: 429, unnamed: 429},
		reply: `{"id":"from-plan","choices":[]}`}
	spare := &keyed{reply: `{"id":"from-spare","choices":[]}`}
	keyedSetup(t, plan, spare)
	if err := provider.AddKey("plan", "", unnamed); err != nil {
		t.Fatal(err)
	}

	s := New()
	code, body := sendChat(s)
	if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, "slow down") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{personalKey, teamKey, unnamed}; !equalStrings(plan.hits, want) {
		t.Fatalf("plan saw keys %v, want %v", plan.hits, want)
	}
	if !equalStrings(spare.hits, []string{"sk-spare-spare"}) {
		t.Fatalf("spare saw keys %v", spare.hits)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" {
		t.Fatalf("recorded %+v", c)
	}
	for _, want := range []string{"plan(个人): ", "plan(团队): ", "plan(" + provider.Mask(unnamed) + "): "} {
		if !strings.Contains(c.Fallback, want) {
			t.Fatalf("record %q lacks %q", c.Fallback, want)
		}
	}
	for _, k := range []string{personalKey, teamKey, unnamed} {
		if strings.Contains(c.Fallback, k) {
			t.Fatalf("a full key reached the call record: %q", c.Fallback)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
