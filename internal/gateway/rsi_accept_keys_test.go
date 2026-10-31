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

type byKey struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.seen = append(b.seen, key)
	lim := b.limited[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if lim {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func (b *byKey) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.Join(b.seen, ",")
	b.seen = nil
	return s
}

func req(s *gateway.Server, model string) string {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Body.String()
}

func env(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func save(t *testing.T, p provider.Provider, url string, extra ...[3]string) {
	t.Helper()
	p.Chat = url + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, e := range extra { // name, key, "off"
		if err := provider.AddKey(p.ID, e[0], e[1]); err != nil {
			t.Fatal(err)
		}
		if e[2] == "off" {
			q, _ := provider.Find(p.ID)
			ks := q.KeyList()
			if err := provider.SetKeyOn(p.ID, ks[len(ks)-1].ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAcceptKeysTakeOverFromEachOther(t *testing.T) {
	env(t)
	up := &byKey{limited: map[string]bool{"k-personal": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	save(t, provider.Provider{ID: "planA", Name: "Plan", Models: []string{"m1"}, Key: "k-personal"}, srv.URL,
		[3]string{"Idle", "k-idle", "off"}, [3]string{"Team", "k-team", ""})
	p, _ := provider.Find("planA")
	if err := provider.RenameKey("planA", p.KeyList()[0].ID, "个人"); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	if body := req(s, "planA/m1"); !strings.Contains(body, "from-k-team") {
		t.Fatalf("body %s", body)
	}
	if got := up.take(); got != "k-personal,k-team" {
		t.Fatalf("tried %s", got)
	}
	c := s.Recent()[0]
	if c.Provider != "planA" || !strings.Contains(c.Fallback, "planA(个人)") || !strings.Contains(c.Fallback, "rate limited") && !strings.Contains(c.Fallback, "429") {
		t.Fatalf("recorded %+v", c)
	}
	// resting: no further attempt on the limited key; idle never tried
	if body := req(s, "planA/m1"); !strings.Contains(body, "from-k-team") || up.take() != "k-team" {
		t.Fatalf("body %s", body)
	}
}

func TestAcceptUnnamedKeyShowsMaskInRecord(t *testing.T) {
	env(t)
	up := &byKey{limited: map[string]bool{"sk-secret-first-1234567890": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	save(t, provider.Provider{ID: "planB", Name: "Plan", Models: []string{"m1"}, Key: "sk-secret-first-1234567890"}, srv.URL,
		[3]string{"", "sk-secret-second-0987654321", ""})
	p, _ := provider.Find("planB")
	mask := p.KeyList()[0].Masked
	s := gateway.New()
	if body := req(s, "planB/m1"); !strings.Contains(body, "from-sk-secret-second") {
		t.Fatalf("body %s", body)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "planB("+mask+")") {
		t.Fatalf("want mask %q in %q", mask, c.Fallback)
	}
	if strings.Contains(c.Fallback, "sk-secret-first-1234567890") || strings.Contains(c.Error, "sk-secret") {
		t.Fatalf("leak %+v", c)
	}
}

func TestAcceptOffKeyNeverUsed(t *testing.T) {
	env(t)
	up := &byKey{limited: map[string]bool{"k-a": true, "k-b": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	save(t, provider.Provider{ID: "planC", Name: "Plan", Models: []string{"m1"}, Key: "k-a"}, srv.URL,
		[3]string{"Free", "k-free", "off"}, [3]string{"B", "k-b", ""})
	s := gateway.New()
	req(s, "planC/m1")
	if got := up.take(); got != "k-a,k-b" {
		t.Fatalf("tried %s", got)
	}
}

func TestAcceptUseKeyChangesGatewayOrder(t *testing.T) {
	env(t)
	up := &byKey{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	save(t, provider.Provider{ID: "planD", Name: "Plan", Models: []string{"m1"}, Key: "k-a"}, srv.URL,
		[3]string{"Team", "k-b", ""})
	p, _ := provider.Find("planD")
	if err := provider.UseKey("planD", p.KeyList()[1].ID); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	if body := req(s, "planD/m1"); !strings.Contains(body, "from-k-b") || up.take() != "k-b" {
		t.Fatalf("body %s", body)
	}
}

func TestAcceptAllKeysFailThenFallbackModel(t *testing.T) {
	env(t)
	up := &byKey{limited: map[string]bool{"k-a": true, "k-b": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	spare := &byKey{}
	srv2 := httptest.NewServer(spare)
	defer srv2.Close()
	save(t, provider.Provider{ID: "spareE", Name: "Spare", Models: []string{"m2"}, Key: "s-1"}, srv2.URL)
	save(t, provider.Provider{ID: "planE", Name: "Plan", Models: []string{"m1"}, Key: "k-a", Fallback: []string{"spareE/m2"}}, srv.URL,
		[3]string{"B", "k-b", ""})
	s := gateway.New()
	if body := req(s, "planE/m1"); !strings.Contains(body, "from-s-1") {
		t.Fatalf("body %s", body)
	}
	if got := up.take(); got != "k-a,k-b" {
		t.Fatalf("tried %s", got)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "planE(") {
		t.Fatalf("%+v", c)
	}
}

func TestAcceptSingleKeyRecordFormatUnchanged(t *testing.T) {
	env(t)
	up := &byKey{limited: map[string]bool{"k-a": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	spare := &byKey{}
	srv2 := httptest.NewServer(spare)
	defer srv2.Close()
	save(t, provider.Provider{ID: "spareF", Name: "Spare", Models: []string{"m2"}, Key: "s-1"}, srv2.URL)
	save(t, provider.Provider{ID: "planF", Name: "Plan", Models: []string{"m1"}, Key: "k-a", Fallback: []string{"spareF/m2"}}, srv.URL)
	s := gateway.New()
	if body := req(s, "planF/m1"); !strings.Contains(body, "from-s-1") {
		t.Fatalf("body %s", body)
	}
	c := s.Recent()[0]
	if !strings.Contains(c.Fallback, "planF: ") || strings.Contains(c.Fallback, "planF(") {
		t.Fatalf("%q", c.Fallback)
	}
}
