package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	multiAcctPersonalKey = "sk-personal-0001"
	multiAcctTeamKey     = "sk-team-0002"
	multiAcctFreeKey     = "sk-free-0003"
	multiAcctSpareKey    = "sk-spare-0009"
)

// multiAcctKeyRouter stands in for one vendor holding several accounts: each
// Authorization key reaches its own fake upstream.
type multiAcctKeyRouter map[string]*fake

func (kr multiAcctKeyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := kr[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, "unknown key", http.StatusUnauthorized)
		return
	}
	f.ServeHTTP(w, r)
}

// setupKeys saves "plan" with the given keys (front key first), falling
// back to "spare", and resets the cooldowns of earlier tests.
func multiAcctSetupKeys(t *testing.T, plan http.Handler, spare *fake, keys []provider.KeySlot) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	planUp := httptest.NewServer(plan)
	t.Cleanup(planUp.Close)
	spareUp := httptest.NewServer(spare)
	t.Cleanup(spareUp.Close)
	p := provider.Provider{ID: "plan", Name: "Plan", Key: keys[0].Key, KeyName: keys[0].Name,
		Keys: keys[1:], Models: []string{"m1"}, Fallback: []string{"spare/m2"}, Chat: planUp.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	sp := provider.Provider{ID: "spare", Name: "Spare", Key: multiAcctSpareKey, Models: []string{"m2"}, Chat: spareUp.URL + "/v1"}
	if err := provider.Save(sp); err != nil {
		t.Fatal(err)
	}
}

func multiAcctSendChat(t *testing.T, s *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

func TestMultiAcctSecondKeyAnswersWhenFirstIsRateLimited(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	team := &fake{t: t, ctype: "application/json", reply: `{"id":"from-team","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team}, spare, []provider.KeySlot{
		{Name: "个人", Key: multiAcctPersonalKey}, {Name: "团队", Key: multiAcctTeamKey}})
	s := New()
	code, body := multiAcctSendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-team") || strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if personal.got == nil || team.got == nil || spare.got != nil {
		t.Fatalf("personal %s, team %s, spare %s", personal.got, team.got, spare.got)
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
	if strings.Contains(c.Fallback, multiAcctPersonalKey) || strings.Contains(c.Error, multiAcctPersonalKey) {
		t.Fatalf("full key recorded: %+v", c)
	}
}

func TestMultiAcctRestingKeyIsSkippedWithinTheMinute(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	team := &fake{t: t, ctype: "application/json", reply: `{"id":"from-team","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team}, spare, []provider.KeySlot{
		{Name: "个人", Key: multiAcctPersonalKey}, {Name: "团队", Key: multiAcctTeamKey}})
	s := New()
	multiAcctSendChat(t, s)
	personal.got, team.got = nil, nil
	if code, body := multiAcctSendChat(t, s); code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if personal.got != nil {
		t.Fatal("a resting key was tried first")
	}
	if team.got == nil {
		t.Fatal("team key was not used")
	}
}

func TestMultiAcctDisabledKeyIsNeverSent(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	team := &fake{t: t, ctype: "application/json", code: 503, reply: `{"error":{"message":"spare down"}}`}
	free := &fake{t: t, ctype: "application/json", reply: `{"id":"from-free","choices":[]}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team, multiAcctFreeKey: free}, spare, []provider.KeySlot{
		{Name: "个人", Key: multiAcctPersonalKey}, {Key: multiAcctTeamKey}, {Name: "免费", Key: multiAcctFreeKey, Off: true}})
	s := New()
	code, body := multiAcctSendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if free.got != nil {
		t.Fatalf("a disabled key was sent a request: %s", free.got)
	}
	if !bytes.Contains(spare.got, []byte(`"model":"m2"`)) {
		t.Fatalf("spare got %s", spare.got)
	}
	c := s.Recent()[0]
	// the unnamed key is named by its mask, and both failures are listed
	if !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "plan("+provider.Mask(multiAcctTeamKey)+"): ") {
		t.Fatalf("recorded %q", c.Fallback)
	}
	if strings.Contains(c.Fallback, multiAcctTeamKey) || strings.Contains(c.Fallback, multiAcctPersonalKey) {
		t.Fatalf("full key recorded: %q", c.Fallback)
	}
}

func TestMultiAcctSingleKeyRecordKeepsItsFormat(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	multiAcctSetupKeys(t, multiAcctKeyRouter{multiAcctPersonalKey: plan}, spare, []provider.KeySlot{{Key: multiAcctPersonalKey}})
	s := New()
	if code, body := multiAcctSendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.HasPrefix(c.Fallback, "plan: ") || strings.Contains(c.Fallback, "(") {
		t.Fatalf("recorded %q", c.Fallback)
	}
}

// A resting key still belongs to its provider's group: the fallback is asked
// only once every key of the primary, resting ones included, has failed.
func TestMultiAcctRestingKeyIsSpentBeforeFallback(t *testing.T) {
	personal := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	team := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	spareHit, personalFirst := false, false
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	planUp := httptest.NewServer(multiAcctKeyRouter{multiAcctPersonalKey: personal, multiAcctTeamKey: team})
	t.Cleanup(planUp.Close)
	spareUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spareHit = true
		personalFirst = personal.got != nil
		spare.ServeHTTP(w, r)
	}))
	t.Cleanup(spareUp.Close)
	p := provider.Provider{ID: "plan", Name: "Plan", Key: multiAcctPersonalKey, KeyName: "个人",
		Keys: []provider.KeySlot{{Name: "团队", Key: multiAcctTeamKey}}, Models: []string{"m1"}, Fallback: []string{"spare/m2"}, Chat: planUp.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: multiAcctSpareKey, Models: []string{"m2"}, Chat: spareUp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	restingUntil.Lock()
	restingUntil.m["plan/"+provider.KeyRef(multiAcctPersonalKey)] = time.Now().Add(time.Minute)
	restingUntil.Unlock()

	s := New()
	if code, body := multiAcctSendChat(t, s); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if team.got == nil {
		t.Fatal("ready team key was not tried")
	}
	if personal.got == nil {
		t.Fatal("resting personal key was not spent before the fallback")
	}
	if !spareHit || !personalFirst {
		t.Fatalf("fallback reached before the resting key was spent: hit %v, personal first %v", spareHit, personalFirst)
	}
}
