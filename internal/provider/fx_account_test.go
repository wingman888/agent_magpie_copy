package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fxRecord is what the stand-in servers below saw in the last request.
type fxRecord struct {
	mu        sync.Mutex
	path      string
	auth      string
	integ     string
	initiator string
	acctID    string
	body      []byte
}

func (r *fxRecord) note(req *http.Request, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.path = req.URL.Path
	r.auth = req.Header.Get("Authorization")
	r.integ = req.Header.Get("Copilot-Integration-Id")
	r.initiator = req.Header.Get("X-Initiator")
	r.acctID = req.Header.Get("chatgpt-account-id")
	r.body = body
}

const fxSSEReply = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
	"data: [DONE]\n\n"

// fxCopilot starts a Copilot stand-in (token exchange, models, chat) and points
// the login at it. The login belongs to user fx-octo.
func fxCopilot(t *testing.T, rec *fxRecord) {
	t.Helper()
	home := sacctEnv(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	sacctWrite(t, filepath.Join(home, "xdg", "github-copilot", "apps.json"),
		`{"github.com:Iv1.fx":{"user":"fx-octo","oauth_token":"gho_fx_oauth"}}`)
	copilotCache = copilotSession{}
	t.Cleanup(func() { copilotCache = copilotSession{} })

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			b, _ := json.Marshal(map[string]any{
				"token":      "tid=fx-session",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]string{"api": srv.URL},
			})
			w.Write(b)
		case "/models":
			rec.note(r, body)
			w.Write([]byte(`{"data":[{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat"},"model_picker_enabled":true}]}`))
		case "/chat/completions":
			rec.note(r, body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, fxSSEReply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = orig })
}

// fxCodex starts a ChatGPT backend stand-in that streams a reply and points
// the Codex login at it. The login is the sacct fixture.
func fxCodex(t *testing.T, rec *fxRecord) {
	t.Helper()
	home := sacctEnv(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	sacctCodexLogin(t, home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.note(r, body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	t.Cleanup(srv.Close)
	orig := CodexBase
	CodexBase = srv.URL
	t.Cleanup(func() { CodexBase = orig })
}

func fxPlainLogin(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Providers {
		if p["id"] == "fxown" || p["id"] == "copilot" {
			return p
		}
	}
	t.Fatalf("no fx entry in %s", b)
	return nil
}

func TestFxCopilotProviderTestSucceeds(t *testing.T) {
	var rec fxRecord
	fxCopilot(t, &rec)
	p := sacctFind(Accounts(), "copilot")
	if p == nil {
		t.Fatal("precondition: copilot login should be found")
	}
	res := p.Test(context.Background())
	if len(res) != 1 {
		t.Fatalf("want one result, got %+v", res)
	}
	if !res[0].OK || res[0].Error != "" || res[0].Protocol != Chat {
		t.Fatalf("copilot test should succeed over chat: %+v", res[0])
	}
	if rec.path != "/chat/completions" || rec.auth != "Bearer tid=fx-session" || rec.integ == "" {
		t.Fatalf("probe not signed with the session token: path=%q auth=%q integ=%q", rec.path, rec.auth, rec.integ)
	}
	if rec.initiator != "user" {
		t.Fatalf("probe ends with a user message, want X-Initiator user, got %q", rec.initiator)
	}
}

func TestFxCodexProviderTestSucceeds(t *testing.T) {
	var rec fxRecord
	fxCodex(t, &rec)
	p := sacctFind(Accounts(), "codex")
	if p == nil {
		t.Fatal("precondition: codex login should be found")
	}
	res := p.Test(context.Background())
	if len(res) != 1 {
		t.Fatalf("want one result, got %+v", res)
	}
	if !res[0].OK || res[0].Error != "" || res[0].Protocol != Responses {
		t.Fatalf("codex test should succeed over responses: %+v", res[0])
	}
	if rec.path != "/responses" || rec.auth != "Bearer "+sacctAccessToken || rec.acctID != "acct-sacct-1" {
		t.Fatalf("probe not signed with the login: path=%q auth=%q account=%q", rec.path, rec.auth, rec.acctID)
	}
	var sent map[string]any
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["stream"] != true || sent["store"] != false {
		t.Fatalf("backend must get stream:true and store:false, got %v", sent)
	}
	if _, ok := sent["input"].([]any); !ok {
		t.Fatalf("input must be a list of items, got %v", sent["input"])
	}
	if _, ok := sent["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens must be removed, got %v", sent)
	}
}

func TestFxPlainProviderStillSignsWithKey(t *testing.T) {
	sacctEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var rec fxRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.note(r, body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	p := Provider{ID: "fxplain", Name: "Fx Plain", Key: "sk-fx-plain", Chat: srv.URL + "/v1", Models: []string{"m1"}}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-fx-plain" {
		t.Fatalf("plain provider must sign with its key, got %q", got)
	}
	if string(p.Prepare([]byte(`{"a":1}`))) != `{"a":1}` {
		t.Fatal("plain provider body must pass through Prepare unchanged")
	}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("plain provider test should still succeed: %+v", res)
	}
	if rec.auth != "Bearer sk-fx-plain" || rec.path != "/v1/chat/completions" {
		t.Fatalf("plain probe: path=%q auth=%q", rec.path, rec.auth)
	}
}

func TestFxSelectionFileHoldsOnlyModels(t *testing.T) {
	var rec fxRecord
	fxCopilot(t, &rec)
	err := Save(Provider{ID: "copilot", Name: "Nope", Chat: "https://evil.example/v1", Key: "sk-fx-leak", Models: []string{"gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"evil.example", "sk-fx-leak", "Nope"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("stored selection leaked %q: %s", leak, b)
		}
	}
	entry := fxPlainLogin(t, Path())
	if len(entry) != 2 || entry["id"] != "copilot" {
		t.Fatalf("selection record should hold id and models only, got %v", entry)
	}
	if p := sacctFind(All(), "copilot"); p == nil || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("copilot should expose only the chosen model: %+v", p)
	}

	if err := Delete("copilot"); err != nil {
		t.Fatal(err)
	}
	p := sacctFind(All(), "copilot")
	if p == nil || len(p.Models) != 0 {
		t.Fatalf("delete should clear the choice and keep the provider: %+v", p)
	}
}

func TestFxOwnProviderKeepsNameAndKey(t *testing.T) {
	sacctEnv(t)
	if err := Save(Provider{ID: "fxown", Name: "Fx Own", Chat: "https://relay.example.com/v1", Key: "k-fx-own"}); err != nil {
		t.Fatal(err)
	}
	entry := fxPlainLogin(t, Path())
	if entry["name"] != "Fx Own" || entry["key"] != "k-fx-own" {
		t.Fatalf("ordinary providers are stored as before, with name and key: %v", entry)
	}
}
