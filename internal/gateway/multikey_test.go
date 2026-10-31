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

// keyedUpstream answers by the bearer key it sees and remembers who came.
type keyedUpstream struct {
	mu   sync.Mutex
	seen []string
	code map[string]int // key -> status; missing = 200
}

func (k *keyedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if c := k.code[key]; c != 0 {
		w.WriteHeader(c)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"ok-`+key+`","choices":[]}`)
}

func (k *keyedUpstream) took() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.seen...)
}

const (
	kPersonal = "sk-personal-AAAA1111"
	kTeam     = "sk-team-BBBB2222"
	kFree     = "sk-free-CCCC3333"
)

func multiSetup(t *testing.T, up *keyedUpstream, fallback string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "plan", Name: "Plan", Key: kPersonal, KeyName: "个人", Models: []string{"m1"}, Chat: srv.URL + "/v1"}
	if fallback != "" {
		p.Fallback = []string{fallback}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("plan", "团队", kTeam); err != nil {
		t.Fatal(err)
	}
}

func sendTo(s *Server) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	return rec.Code, rec.Body.String()
}

func TestKeyFailoverAndCooldown(t *testing.T) {
	up := &keyedUpstream{code: map[string]int{kPersonal: 429}}
	multiSetup(t, up, "")
	s := New()
	code, body := sendTo(s)
	if code != 200 || !strings.Contains(body, "ok-"+kTeam) {
		t.Fatalf("%d %s", code, body)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "plan(个人): ") || !strings.Contains(c.Fallback, "rate limited") {
		t.Fatalf("log %+v", c)
	}
	if strings.Contains(c.Fallback, kPersonal) || strings.Contains(c.Fallback, kTeam) {
		t.Fatal("key leaked into log")
	}
	before := len(up.took())
	if code, body = sendTo(s); code != 200 || !strings.Contains(body, "ok-"+kTeam) {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.took()[before:]; len(got) != 1 || got[0] != kTeam {
		t.Fatalf("cooled key tried first: %v", got)
	}
}

func TestMaskedLabelWithoutName(t *testing.T) {
	up := &keyedUpstream{code: map[string]int{kPersonal: 429}}
	multiSetup(t, up, "")
	p, _ := provider.Find("plan")
	if err := provider.RenameKey("plan", p.KeyList()[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	sendTo(s)
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "plan("+provider.Mask(kPersonal)+"): ") || strings.Contains(c.Fallback, kPersonal) {
		t.Fatalf("log %+v", c)
	}
}

func TestDisabledKeyNeverUsed(t *testing.T) {
	up := &keyedUpstream{code: map[string]int{kPersonal: 429, kTeam: 429}}
	multiSetup(t, up, "")
	if err := provider.AddKey("plan", "免费", kFree); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("plan")
	if err := provider.SetKeyOn("plan", p.KeyList()[2].ID, false); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, _ := sendTo(s)
	sendTo(s)
	if code != 429 {
		t.Fatalf("code %d", code)
	}
	for _, k := range up.took() {
		if k == kFree {
			t.Fatal("disabled key reached upstream")
		}
	}
}

func TestAllKeysFailThenFallbackModel(t *testing.T) {
	up := &keyedUpstream{code: map[string]int{kPersonal: 429, kTeam: 429}}
	multiSetup(t, up, "spare/m2")
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	sp := httptest.NewServer(spare)
	t.Cleanup(sp.Close)
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}, Chat: sp.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.took(); len(got) != 2 || got[0] != kPersonal || got[1] != kTeam {
		t.Fatalf("tried %v", got)
	}
	if c := s.Recent()[0]; strings.Count(c.Fallback, "plan(") != 2 {
		t.Fatalf("log %+v", c)
	}
}
