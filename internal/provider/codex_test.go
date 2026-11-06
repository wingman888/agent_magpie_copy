package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeCodexAuth writes a ChatGPT-mode auth.json with an access token that
// expires at exp, plus fields dial must not touch.
func writeCodexAuth(t *testing.T, exp time.Time) {
	t.Helper()
	body := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         map[string]any{"keep": "me"},
		"tokens": map[string]any{
			"id_token":      fakeJWT(t, map[string]any{"email": "dev@example.com"}),
			"access_token":  fakeJWT(t, map[string]any{"exp": exp.Unix()}),
			"refresh_token": "old-refresh",
			"account_id":    "acct-123",
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, homeFile(".codex", "auth.json"), string(b))
}

// fakeTokenServer stands in for OpenAI's token endpoint.
// renewedAccess is the access token the stand-in issues. It has no exp claim,
// so dial never renews it again.
const renewedAccess = "renewed-access"

func fakeTokenServer(t *testing.T, status int, hits *int32, got *map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		if got != nil {
			*got = m
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  renewedAccess,
			"refresh_token": "new-refresh",
		})
	}))
	t.Cleanup(srv.Close)
	old := CodexTokenURL
	CodexTokenURL = srv.URL
	t.Cleanup(func() { CodexTokenURL = old })
}

func TestPrepareCodexRewritesBody(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	in := []byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":100,"temperature":0.2,` +
		`"top_p":0.9,"max_completion_tokens":5,"reasoning":{"effort":"low"},"stream":false,"store":true}`)
	var m map[string]any
	if err := json.Unmarshal(p.Prepare(in), &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "max_completion_tokens"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s not removed", k)
		}
	}
	if _, ok := m["reasoning"]; !ok {
		t.Error("reasoning dropped")
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v", m["input"])
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["type"] != "input_text" || content["text"] != "hi" {
		t.Errorf("input item = %#v", item)
	}
}

func TestPrepareCodexKeepsListInput(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	in := `{"model":"gpt-5.5","input":[{"role":"user","content":"x"}]}`
	var m map[string]any
	json.Unmarshal(p.Prepare([]byte(in)), &m)
	if items, ok := m["input"].([]any); !ok || len(items) != 1 {
		t.Errorf("input = %#v", m["input"])
	}
}

func TestCodexRenewWritesBackAndKeepsFields(t *testing.T) {
	setupEnv(t)
	writeCodexAuth(t, time.Now().Add(-time.Minute))
	var hits int32
	var got map[string]any
	fakeTokenServer(t, http.StatusOK, &hits, &got)

	p := findAccount(Accounts(), "codex")
	req := newReq(t)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("token endpoint hit %d times", hits)
	}
	if got["grant_type"] != "refresh_token" || got["refresh_token"] != "old-refresh" || got["client_id"] != codexClientID {
		t.Errorf("refresh request = %v", got)
	}
	if h := req.Header.Get("Authorization"); h != "Bearer "+renewedAccess {
		t.Errorf("Authorization = %q", h)
	}

	b, err := os.ReadFile(homeFile(".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	tokens := f["tokens"].(map[string]any)
	if tokens["refresh_token"] != "new-refresh" {
		t.Errorf("refresh_token = %v", tokens["refresh_token"])
	}
	if tokens["account_id"] != "acct-123" || tokens["id_token"] == nil {
		t.Errorf("tokens lost fields: %v", tokens)
	}
	if f["auth_mode"] != "chatgpt" || f["custom"].(map[string]any)["keep"] != "me" {
		t.Errorf("file lost fields: %v", f)
	}
	if _, ok := f["OPENAI_API_KEY"]; !ok {
		t.Error("OPENAI_API_KEY dropped")
	}
	if st, err := os.Stat(homeFile(".codex", "auth.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", st.Mode().Perm(), err)
	}
}

func TestCodexRenewFailureAsksForSignIn(t *testing.T) {
	setupEnv(t)
	writeCodexAuth(t, time.Now().Add(-time.Minute))
	var hits int32
	fakeTokenServer(t, http.StatusBadRequest, &hits, nil)

	p := findAccount(Accounts(), "codex")
	err := p.Sign(context.Background(), newReq(t), Responses, nil)
	if !errors.Is(err, ErrSignedOut) {
		t.Fatalf("want ErrSignedOut, got %v", err)
	}
	if !strings.Contains(err.Error(), `run "codex login" again`) {
		t.Errorf("message = %q", err.Error())
	}
}

func TestCodexRenewHappensOnceUnderConcurrency(t *testing.T) {
	setupEnv(t)
	writeCodexAuth(t, time.Now().Add(-time.Minute))
	var hits int32
	fakeTokenServer(t, http.StatusOK, &hits, nil)

	p := findAccount(Accounts(), "codex")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	reqs := make([]*http.Request, len(errs))
	for i := range reqs {
		reqs[i] = newReq(t)
	}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.Sign(context.Background(), reqs[i], Responses, nil)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("token endpoint hit %d times, want 1", n)
	}
}

func TestCodexFreshTokenIsNotRenewed(t *testing.T) {
	setupEnv(t)
	writeCodexAuth(t, time.Now().Add(time.Hour))
	var hits int32
	fakeTokenServer(t, http.StatusOK, &hits, nil)

	p := findAccount(Accounts(), "codex")
	if err := p.Sign(context.Background(), newReq(t), Responses, nil); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("token endpoint hit %d times for a fresh token", hits)
	}
}
