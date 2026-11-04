package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyedUpstream answers by the key it is called with, so one upstream can
// stand for several accounts of the same provider.
type keyedUpstream map[string]*fake

func (m keyedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := m[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, "unknown key", http.StatusUnauthorized)
		return
	}
	f.ServeHTTP(w, r)
}

// acct is one account of the test provider "plan", in its order of use.
type acct struct {
	name, key string
	off       bool
}

// setupAccounts saves "plan" with the given accounts, all served by up, and
// "spare" as its fallback, served by spare. Each test uses its own key values:
// the cooldown of a key lasts for the whole test binary.
func setupAccounts(t *testing.T, up keyedUpstream, spare *fake, accts ...acct) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()

	planUp := httptest.NewServer(up)
	t.Cleanup(planUp.Close)
	spareUp := httptest.NewServer(spare)
	t.Cleanup(spareUp.Close)

	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "spare-key", Models: []string{"m2"}, Chat: spareUp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	plan := provider.Provider{ID: "plan", Name: "Plan", Key: accts[0].key, KeyName: accts[0].name,
		Models: []string{"m1"}, Fallback: []string{"spare/m2"}, Chat: planUp.URL + "/v1"}
	if err := provider.Save(plan); err != nil {
		t.Fatal(err)
	}
	for _, a := range accts[1:] {
		if err := provider.AddKey("plan", a.name, a.key); err != nil {
			t.Fatal(err)
		}
	}
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	list := p.KeyList()
	if len(list) != len(accts) {
		t.Fatalf("keys %+v, want %d", list, len(accts))
	}
	for i, a := range accts {
		if a.off {
			if err := provider.SetKeyOn("plan", list[i].ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func sendChat(t *testing.T, s *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

func rateLimited() *fake {
	return &fake{ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
}

func answers(id string) *fake {
	return &fake{ctype: "application/json", reply: `{"id":"` + id + `","choices":[]}`}
}

func TestKeysFailOverToTheNextAccount(t *testing.T) {
	personal, team := rateLimited(), answers("from-team")
	up := keyedUpstream{"t1-personal": personal, "t1-team": team}
	spare := answers("from-spare")
	setupAccounts(t, up, spare, acct{"个人", "t1-personal", false}, acct{"团队", "t1-team", false})

	s := New()
	code, body := sendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-team") || strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if spare.got != nil {
		t.Fatal("the fallback was used while another account was left")
	}
	c := s.Recent()[0]
	if c.Provider != "plan" || !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("recorded %+v", c)
	}
}

func TestKeysRestingAccountIsSkippedWithinAMinute(t *testing.T) {
	personal, team := rateLimited(), answers("from-team")
	up := keyedUpstream{"t2-personal": personal, "t2-team": team}
	setupAccounts(t, up, answers("from-spare"), acct{"个人", "t2-personal", false}, acct{"团队", "t2-team", false})

	s := New()
	sendChat(t, s)
	personal.got, team.got = nil, nil
	code, body := sendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-team") {
		t.Fatalf("%d %s", code, body)
	}
	if personal.got != nil {
		t.Fatalf("the resting account was tried first: %s", personal.got)
	}
	if team.got == nil {
		t.Fatal("the other account was not used")
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("recorded a failure for a skipped account: %+v", c)
	}
}

func TestDisabledKeyIsNeverSent(t *testing.T) {
	personal, team, free := rateLimited(), answers("from-team"), answers("from-free")
	up := keyedUpstream{"t3-personal": personal, "t3-team": team, "t3-free": free}
	setupAccounts(t, up, answers("from-spare"),
		acct{"个人", "t3-personal", false}, acct{"团队", "t3-team", false}, acct{"免费", "t3-free", true})

	s := New()
	for i := 0; i < 2; i++ {
		if code, body := sendChat(t, s); code != 200 || !strings.Contains(body, "from-team") {
			t.Fatalf("%d %s", code, body)
		}
	}
	if free.got != nil {
		t.Fatalf("a disabled key was sent: %s", free.got)
	}
}

func TestFallbackAfterEveryAccountFails(t *testing.T) {
	personal, team := rateLimited(), &fake{ctype: "application/json", code: 429, reply: `{"error":{"message":"team quota used up"}}`}
	up := keyedUpstream{"t4-personal": personal, "t4-team": team}
	spare := answers("from-spare")
	setupAccounts(t, up, spare, acct{"个人", "t4-personal", false}, acct{"团队", "t4-team", false})

	s := New()
	code, body := sendChat(t, s)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if personal.got == nil || team.got == nil {
		t.Fatal("an account was skipped before the fallback")
	}
	if !strings.Contains(string(spare.got), `"model":"m2"`) {
		t.Fatalf("spare got %s", spare.got)
	}
	c := s.Recent()[0]
	if c.Provider != "spare" || !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "plan(团队): ") {
		t.Fatalf("recorded %+v", c)
	}
}
