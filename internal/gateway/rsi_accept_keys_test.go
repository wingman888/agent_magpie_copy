package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

type keyUp struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string
}

func (b *keyUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.seen = append(b.seen, key)
	lim := b.limited[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if lim {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited for `+strings.ToUpper(key)+`"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func (b *keyUp) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.Join(b.seen, ",")
	b.seen = nil
	return s
}

func env(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func save(t *testing.T, p provider.Provider, up http.Handler) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p.Chat = srv.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func send(s *gateway.Server, model string) (int, string) {
	rec := httptest.NewRecorder()
	req := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(req)))
	return rec.Code, rec.Body.String()
}

func TestAcceptKeysTakeOverAndRest(t *testing.T) {
	env(t)
	up := &keyUp{limited: map[string]bool{"k-personal": true}}
	save(t, provider.Provider{ID: "acc-plan", Name: "Plan", Models: []string{"m1"},
		Key: "k-personal", KeyName: "Personal",
		Keys: []provider.KeyAccount{{Name: "Idle", Key: "k-idle", Off: true}, {Name: "Team", Key: "k-team"}}}, up)
	s := gateway.New()
	if code, body := send(s, "acc-plan/m1"); code != 200 || !strings.Contains(body, "from-k-team") {
		t.Fatalf("%d %s", code, body)
	}
	if c := s.Recent()[0]; c.Provider != "acc-plan" || !strings.Contains(c.Fallback, "acc-plan (Personal): ") {
		t.Fatalf("recorded %+v", c)
	}
	up.take()
	if _, body := send(s, "acc-plan/m1"); !strings.Contains(body, "from-k-team") {
		t.Fatal(body)
	}
	if got := up.take(); got != "k-team" {
		t.Fatalf("tried %q", got)
	}
}

func TestAcceptKeysUnnamedLabelIsMasked(t *testing.T) {
	env(t)
	up := &keyUp{limited: map[string]bool{"sk-first-aaaaaaaa": true}}
	save(t, provider.Provider{ID: "acc-mask", Name: "Mask", Models: []string{"m1"},
		Key: "sk-first-aaaaaaaa", Keys: []provider.KeyAccount{{Key: "sk-second-bbbbbbbb"}}}, up)
	s := gateway.New()
	if code, body := send(s, "acc-mask/m1"); code != 200 || !strings.Contains(body, "from-sk-second-bbbbbbbb") {
		t.Fatalf("%d %s", code, body)
	}
	fb := s.Recent()[0].Fallback
	if !strings.Contains(fb, "acc-mask ("+provider.Mask("sk-first-aaaaaaaa")+"): ") || strings.Contains(fb, "sk-first-aaaaaaaa") {
		t.Fatalf("fallback %q", fb)
	}
}

func TestAcceptAllKeysFailLastErrorReturned(t *testing.T) {
	env(t)
	up := &keyUp{limited: map[string]bool{"k1": true, "k2": true}}
	save(t, provider.Provider{ID: "acc-dead", Name: "Dead", Models: []string{"m1"},
		Key: "k1", Keys: []provider.KeyAccount{{Name: "Two", Key: "k2"}}}, up)
	s := gateway.New()
	code, body := send(s, "acc-dead/m1")
	if code != 429 || !strings.Contains(body, "rate limited for K2") {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.take(); got != "k1,k2" {
		t.Fatalf("tried %q", got)
	}
}

func TestAcceptKeysBeforeFallbackModel(t *testing.T) {
	env(t)
	planUp := &keyUp{limited: map[string]bool{"p1": true, "p2": true}}
	spareUp := &keyUp{limited: map[string]bool{"s1": true}}
	save(t, provider.Provider{ID: "acc-fplan", Name: "FPlan", Models: []string{"m1"},
		Key: "p1", Keys: []provider.KeyAccount{{Name: "P2", Key: "p2"}}, Fallback: []string{"acc-fspare/m2"}}, planUp)
	save(t, provider.Provider{ID: "acc-fspare", Name: "FSpare", Models: []string{"m2"},
		Key: "s1", Keys: []provider.KeyAccount{{Name: "S2", Key: "s2"}}}, spareUp)
	s := gateway.New()
	code, body := send(s, "acc-fplan/m1")
	if code != 200 || !strings.Contains(body, "from-s2") {
		t.Fatalf("%d %s", code, body)
	}
	if got := planUp.take(); got != "p1,p2" {
		t.Fatalf("plan tried %q", got)
	}
	if got := spareUp.take(); got != "s1,s2" {
		t.Fatalf("spare tried %q", got)
	}
}

func TestAcceptSingleKeyKeepsProviderIdentity(t *testing.T) {
	env(t)
	up := &keyUp{limited: map[string]bool{}}
	save(t, provider.Provider{ID: "acc-solo", Name: "Solo", Models: []string{"m1"}, Key: "only"}, up)
	s := gateway.New()
	if code, body := send(s, "acc-solo/m1"); code != 200 || !strings.Contains(body, "from-only") {
		t.Fatalf("%d %s", code, body)
	}
	if c := s.Recent()[0]; c.Provider != "acc-solo" || c.Fallback != "" {
		t.Fatalf("%+v", c)
	}
}
