package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// fakeJWT is an unsigned token whose payload is claims; dial reads payloads only.
func acc_fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// setupHome points every credential and config location at a fresh directory.
func acc_setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func acc_writeFixture(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func acc_readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// signInCodex writes a ChatGPT login for ada@example.com, with an access token
// that expires at exp. It returns the access token.
func acc_signInCodex(t *testing.T, home string, exp time.Time) string {
	t.Helper()
	access := acc_fakeJWT(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	id := acc_fakeJWT(map[string]any{"email": "ada@example.com"})
	acc_writeFixture(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"other":          "keep me",
		"tokens":         map[string]any{"id_token": id, "access_token": access, "refresh_token": "rt-old", "account_id": "acct-1"},
	})
	return access
}

// signInCopilot writes a GitHub login into the editor's apps.json.
func acc_signInCopilot(t *testing.T, home string) {
	t.Helper()
	acc_writeFixture(t, filepath.Join(home, "xdg-config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.abc": map[string]any{"user": "octo", "oauth_token": "gho_test"},
	})
}

// setString swaps a package variable for the test's lifetime.
func acc_setString(t *testing.T, dst *string, v string) {
	t.Helper()
	old := *dst
	*dst = v
	t.Cleanup(func() { *dst = old })
}

func acc_ids(ms []catalog.Model) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func acc_sameIDs(a, b []string) bool {
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

func TestAcc_CodexSignedInIsOffered(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Hour))
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Login != "ada@example.com" || p.Plan != "plus" || !p.Ready() {
		t.Fatalf("codex provider = %+v", *p)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex/gpt-5.5 is missing from the catalog")
	}
}

func TestAcc_APIKeyLoginIsNotAnAccount(t *testing.T) {
	home := acc_setupHome(t)
	acc_writeFixture(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-test"})
	if _, err := Find("codex"); err == nil {
		t.Fatal("an API-key Codex login was offered as an account")
	}
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("an API-key Codex login appears in All()")
		}
	}
}

func TestAcc_ClaudeIsDetectedNeverOffered(t *testing.T) {
	home := acc_setupHome(t)
	acc_writeFixture(t, filepath.Join(home, "claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-secret"}})
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude appears in All()")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatalf("claude model %s in the catalog", e.ID)
		}
	}
	us := UnavailableAgents()
	if len(us) != 1 || us[0].Agent != "claude" || us[0].Reason == "" {
		t.Fatalf("UnavailableAgents() = %+v", us)
	}
	if err := Save(Provider{ID: "claude", Chat: "https://x.example/v1", Key: "k"}); err == nil {
		t.Fatal("Save accepted claude")
	}
	if _, err := Find("claude"); err == nil {
		t.Fatal("Find returned claude")
	}
	b, _ := json.Marshal(All())
	if strings.Contains(string(b), "sk-ant-secret") {
		t.Fatal("Claude's token leaked into the provider list")
	}
}

func TestAcc_CodexRequestShape(t *testing.T) {
	home := acc_setupHome(t)
	access := acc_signInCodex(t, home, time.Now().Add(time.Hour))
	acc_setString(t, &CodexBase, "https://codex.test/backend-api/codex")
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"stream":false}`
	up, err := Route(context.Background(), *p, Responses, "/responses", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if up.URL != CodexBase+"/responses" {
		t.Fatalf("URL = %s", up.URL)
	}
	if up.Header["Authorization"] != "Bearer "+access || up.Header["chatgpt-account-id"] != "acct-1" {
		t.Fatalf("headers = %v", up.Header)
	}
	var got map[string]any
	if err := json.Unmarshal(up.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.5" || got["stream"] != true || got["store"] != false {
		t.Fatalf("body = %s", up.Body)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s was sent: %s", k, up.Body)
		}
	}
	items, _ := got["input"].([]any)
	if len(items) != 1 {
		t.Fatalf("input is not a one-item list: %s", up.Body)
	}
	item, _ := items[0].(map[string]any)
	content, _ := item["content"].([]any)
	if len(content) != 1 || item["role"] != "user" {
		t.Fatalf("input item = %v", item)
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Fatalf("input part = %v", part)
	}
}

func TestAcc_CodexRenewsAndKeepsOtherFields(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Minute)) // inside the 5-minute window
	newAccess := acc_fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		if req["refresh_token"] != "rt-old" || req["grant_type"] != "refresh_token" || req["client_id"] != codexClientID {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	t.Cleanup(srv.Close)
	acc_setString(t, &codexRefreshURL, srv.URL)

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	up, err := Route(context.Background(), *p, Responses, "/responses", []byte(`{"model":"codex/gpt-5.5","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.Header["Authorization"] != "Bearer "+newAccess {
		t.Fatalf("request did not use the renewed token: %v", up.Header)
	}
	var file map[string]any
	if err := json.Unmarshal([]byte(acc_readFile(t, filepath.Join(home, ".codex", "auth.json"))), &file); err != nil {
		t.Fatal(err)
	}
	tokens, _ := file["tokens"].(map[string]any)
	if file["other"] != "keep me" || tokens["access_token"] != newAccess || tokens["refresh_token"] != "rt-new" ||
		tokens["account_id"] != "acct-1" || tokens["id_token"] == "" || file["auth_mode"] != "chatgpt" {
		t.Fatalf("auth.json after renewal = %v", file)
	}
}

func TestAcc_CodexRenewalRefusedSaysLoggedOut(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Minute))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	t.Cleanup(srv.Close)
	acc_setString(t, &codexRefreshURL, srv.URL)
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Route(context.Background(), *p, Responses, "/responses", []byte(`{"model":"codex/gpt-5.5"}`))
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v, want a sign-in hint", err)
	}
}

func TestAcc_SaveWritesOnlyModelPicks(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Name: "Codex", Key: "secret-key", Models: []string{"gpt-5.5", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw := acc_readFile(t, Path())
	for _, bad := range []string{"secret-key", `"name"`, `"key"`, `"chat"`} {
		if strings.Contains(raw, bad) {
			t.Fatalf("config holds %s: %s", bad, raw)
		}
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := acc_ids(p.Exposed()); !acc_sameIDs(got, []string{"gpt-5.5"}) {
		t.Fatalf("exposed = %v", got)
	}
}

func TestAcc_DeleteOnlyClearsPicks(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatalf("codex vanished after Delete: %v", err)
	}
	if len(p.Models) != 0 || p.Login == "" {
		t.Fatalf("provider after Delete = %+v", *p)
	}
	if n := len(p.Exposed()); n < 2 {
		t.Fatalf("after clearing picks, exposed = %d models, want the built-in list", n)
	}
	if !strings.Contains(acc_readFile(t, filepath.Join(home, ".codex", "auth.json")), "rt-old") {
		t.Fatal("Delete touched the Codex login")
	}
}

func TestAcc_LoggingOutHidesTheAccount(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCodex(t, home, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("codex still found after logout")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("%s still in the catalog after logout", e.ID)
		}
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves after logout")
	}
}

func TestAcc_CopilotModelFilter(t *testing.T) {
	body := `{"data":[
	 {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
	 {"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
	 {"id":"text-embedding-3-small","name":"Embedding","model_picker_enabled":false,"capabilities":{"type":"embeddings"}},
	 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
	 {"id":"internal-router","name":"Internal Router","model_picker_enabled":true,"capabilities":{"type":"chat"}},
	 {"id":"hidden-model","name":"Hidden","model_picker_enabled":false,"capabilities":{"type":"chat"}},
	 {"id":"gpt-5-mini","name":"GPT-5 mini","model_picker_enabled":true,"capabilities":{"type":"chat"}}
	]}`
	ms, err := copilotChatModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := acc_ids(ms); !acc_sameIDs(got, []string{"gpt-4.1", "gpt-5-mini"}) {
		t.Fatalf("chat models = %v", got)
	}
}

func TestAcc_CopilotBuiltinListBeforeRefresh(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if p.Login != "octo" || !p.Ready() {
		t.Fatalf("copilot provider = %+v", *p)
	}
	got := acc_ids(p.Available())
	if len(got) == 0 {
		t.Fatal("built-in Copilot list is empty")
	}
	for _, id := range got {
		if id == "auto" {
			t.Fatal("built-in Copilot list contains auto")
		}
	}
	if _, ok := p.Fetched(); ok {
		t.Fatal("Fetched reports a refresh that never happened")
	}
}

func TestAcc_CopilotRefreshAndRoute(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			if r.Header.Get("Authorization") != "token gho_test" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-1", "endpoints": map[string]string{"api": srv.URL}})
		case "/models":
			w.Write([]byte(`{"data":[{"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}},` +
				`{"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	acc_setString(t, &copilotTokenURL, srv.URL+"/copilot_internal/v2/token")

	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !acc_sameIDs(acc_ids(ms), []string{"gpt-4.1"}) {
		t.Fatalf("fetched = %v", acc_ids(ms))
	}
	if _, ok := p.Fetched(); !ok {
		t.Fatal("the refreshed list was not kept")
	}
	p, _ = Find("copilot")
	if !acc_sameIDs(acc_ids(p.Available()), []string{"gpt-4.1"}) {
		t.Fatalf("available after refresh = %v", acc_ids(p.Available()))
	}

	up, err := Route(context.Background(), *p, Chat, "/chat/completions",
		[]byte(`{"model":"copilot/gpt-4.1","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.URL != srv.URL+"/chat/completions" || up.Header["Authorization"] != "Bearer sess-1" {
		t.Fatalf("route = %s %v", up.URL, up.Header)
	}
	if up.Header["X-Initiator"] != "user" || up.Header["Copilot-Vision-Request"] != "" {
		t.Fatalf("user turn headers = %v", up.Header)
	}

	up, err = Route(context.Background(), *p, Chat, "/chat/completions", []byte(`{"model":"gpt-4.1","messages":[`+
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},`+
		`{"role":"assistant","content":"ok"},{"role":"tool","tool_call_id":"c1","content":"done"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.Header["X-Initiator"] != "agent" || up.Header["Copilot-Vision-Request"] != "true" {
		t.Fatalf("tool turn with image headers = %v", up.Header)
	}
}

func TestAcc_CopilotExpiredSessionSaysSignIn(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	t.Cleanup(srv.Close)
	acc_setString(t, &copilotTokenURL, srv.URL)
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Route(context.Background(), *p, Chat, "/chat/completions", []byte(`{"model":"gpt-4.1","messages":[]}`))
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v, want a sign-in hint", err)
	}
}

func TestAcc_CollectResponseFromStream(t *testing.T) {
	raw := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r0\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n"
	out, err := CollectResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v["id"] != "r1" || v["status"] != "completed" {
		t.Fatalf("collected = %s", out)
	}
}

func TestAcc_CollectResponseFailure(t *testing.T) {
	raw := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"model overloaded\"}}}\n\n"
	if _, err := CollectResponse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err = %v", err)
	}
}

// Picks of an account provider are kept only while the account offers them.
func TestApk_AccountPicksFollowWhatTheAccountOffers(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	if err := catalog.SaveLive(copilotID, copilotDefaultAPI, []catalog.Model{
		{ID: "gpt-4.1", Name: "GPT-4.1", Provider: "github-copilot"},
		{ID: "gpt-5-mini", Name: "GPT-5 mini", Provider: "github-copilot"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "copilot", Models: []string{"auto", "gpt-4.1", "retired-model"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if got := acc_ids(p.Exposed()); !acc_sameIDs(got, []string{"gpt-4.1"}) {
		t.Fatalf("exposed = %v, want only the picks the account offers", got)
	}
	for _, e := range Catalog() {
		if e.ID == "copilot/auto" || e.ID == "copilot/retired-model" {
			t.Fatalf("%s reached the catalog", e.ID)
		}
	}
}

// An account provider with no picks exposes everything it offers, uncapped.
func TestApk_AccountWithoutPicksIsNotCapped(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	var many []catalog.Model
	for i := 0; i < manyModels+6; i++ {
		id := fmt.Sprintf("model-%02d", i)
		many = append(many, catalog.Model{ID: id, Name: id, Provider: "github-copilot"})
	}
	if err := catalog.SaveLive(copilotID, copilotDefaultAPI, many); err != nil {
		t.Fatal(err)
	}
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Exposed()); n != len(many) {
		t.Fatalf("exposed %d of %d models, want all of them", n, len(many))
	}
}

// The built-in Copilot list never offers auto, even when picked.
func TestApk_CopilotAutoNeverExposed(t *testing.T) {
	home := acc_setupHome(t)
	acc_signInCopilot(t, home)
	if err := Save(Provider{ID: "copilot", Models: []string{"auto"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range p.Exposed() {
		if m.ID == "auto" {
			t.Fatal("auto exposed")
		}
	}
}

// A refused Copilot session says how to sign in again; Codex is not checked this way.
func TestApk_RefusedCopilotSessionSaysSignIn(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		msg, ok := SessionRefused(Provider{ID: copilotID, Agent: copilotID}, status)
		if !ok || !strings.Contains(msg, "请重新登录 Copilot") {
			t.Fatalf("status %d: got %q, %v", status, msg, ok)
		}
	}
	if _, ok := SessionRefused(Provider{ID: codexID, Agent: codexID}, http.StatusUnauthorized); ok {
		t.Fatal("codex refusal should not be mapped")
	}
	if _, ok := SessionRefused(Provider{ID: copilotID, Agent: copilotID}, http.StatusOK); ok {
		t.Fatal("200 should not be mapped")
	}
}
