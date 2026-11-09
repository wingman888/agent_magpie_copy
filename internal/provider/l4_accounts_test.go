package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// l4AcctTEnv points HOME, XDG dirs and CLAUDE_CONFIG_DIR at a fresh temp tree.
func l4AcctTEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return root
}

// l4AcctTJWT builds an unsigned JWT with the given payload.
func l4AcctTJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

func l4AcctTWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func l4AcctTCodexAuth(t *testing.T, mode string) string {
	t.Helper()
	exp := time.Now().Add(2 * time.Hour).Unix()
	idTok := l4AcctTJWT(t, map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	accTok := l4AcctTJWT(t, map[string]any{"exp": exp})
	body := `{"auth_mode":"` + mode + `","OPENAI_API_KEY":null,"tokens":{"id_token":"` + idTok +
		`","access_token":"` + accTok + `","refresh_token":"rt-1","account_id":"acct-9"},"last_refresh":"x"}`
	path := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	l4AcctTWrite(t, path, body)
	return path
}

func l4AcctTCopilotLogin(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")
	l4AcctTWrite(t, path, `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
}

func l4AcctTFind(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func l4AcctTHasModel(ms []catalog.Model, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

func TestL4AcctTCodexJWTAndAccount(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCodexAuth(t, "chatgpt")
	p := l4AcctTFind(All(), CodexID)
	if p == nil {
		t.Fatal("codex should be listed when signed in")
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Plan != "plus" || p.Account.Agent != "Codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if !p.Ready() {
		t.Fatal("account provider should be ready without a key")
	}
	if p.Key != "" {
		t.Fatal("account provider must not carry a key")
	}
}

func TestL4AcctTCodexAPIKeyModeIsNotAccount(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCodexAuth(t, "apikey")
	if l4AcctTFind(All(), CodexID) != nil {
		t.Fatal("apikey-mode Codex login must not be an account provider")
	}
}

func TestL4AcctTSaveDeleteOnlyTouchesModels(t *testing.T) {
	l4AcctTEnv(t)
	authPath := l4AcctTCodexAuth(t, "chatgpt")
	before, _ := os.ReadFile(authPath)

	if err := Save(Provider{ID: CodexID, Name: "Ignored", Key: "sk-should-not-store", Models: []string{"gpt-5.5", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path())
	s := string(raw)
	if strings.Contains(s, "sk-should-not-store") || strings.Contains(s, "Ignored") || strings.Contains(s, "\"key\"") || strings.Contains(s, "\"name\"") {
		t.Fatalf("config stores more than models: %s", s)
	}
	p := l4AcctTFind(All(), CodexID)
	if p == nil || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("saved models not applied: %+v", p)
	}

	if err := Delete(CodexID); err != nil {
		t.Fatal(err)
	}
	p = l4AcctTFind(All(), CodexID)
	if p == nil {
		t.Fatal("deleting an account provider must not remove it while still signed in")
	}
	if len(p.Models) != 0 {
		t.Fatalf("models not cleared: %v", p.Models)
	}
	after, _ := os.ReadFile(authPath)
	if string(before) != string(after) {
		t.Fatal("save/delete must not write the agent's credentials")
	}
}

func TestL4AcctTSaveRequiresSignIn(t *testing.T) {
	l4AcctTEnv(t)
	if err := Save(Provider{ID: CopilotID, Models: []string{"gpt-4.1"}}); err == nil {
		t.Fatal("saving an account provider that is not signed in should fail")
	}
}

func TestL4AcctTResidualTicksIgnoredWhenSignedOut(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTWrite(t, Path(), `{"providers":[{"id":"codex","models":["gpt-5.5"]},{"id":"copilot","models":["gpt-4.1"]}]}`)
	for _, p := range All() {
		if IsAccount(p.ID) {
			t.Fatalf("signed-out account %s listed: %+v", p.ID, p)
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") || strings.HasPrefix(e.ID, "copilot/") {
			t.Fatalf("residual tick became catalog entry %s", e.ID)
		}
	}
}

func TestL4AcctTClaudeNeverAProvider(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTWrite(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-claude-token"}}`)
	if l4AcctTFind(All(), ClaudeID) != nil {
		t.Fatal("claude must never be listed as a provider")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatalf("claude in catalog: %s", e.ID)
		}
	}
	note, ok := ClaudeNotice()
	if !ok || !strings.Contains(note, "已登录") || !strings.Contains(note, "不作为 Provider 提供") {
		t.Fatalf("ClaudeNotice = %q, %v", note, ok)
	}
	if strings.Contains(note, "secret-claude-token") {
		t.Fatal("notice must not expose credentials")
	}
	if err := Save(Provider{ID: ClaudeID, Chat: "https://example.com/v1", Key: "k"}); err == nil {
		t.Fatal("claude must not be savable as a provider")
	}
}

func TestL4AcctTClaudeNoticeAbsentWhenSignedOut(t *testing.T) {
	l4AcctTEnv(t)
	if _, ok := ClaudeNotice(); ok {
		t.Fatal("no notice expected without a Claude Code login")
	}
}

func TestL4AcctTCopilotBuiltinHasNoAuto(t *testing.T) {
	for _, m := range catalog.Builtin(CopilotID) {
		if m.ID == "auto" {
			t.Fatal("copilot built-in list must not contain auto")
		}
	}
	if len(catalog.Builtin(CopilotID)) == 0 {
		t.Fatal("copilot built-in list must not be empty")
	}
	l4AcctTEnv(t)
	l4AcctTCopilotLogin(t)
	p := l4AcctTFind(All(), CopilotID)
	if p == nil {
		t.Fatal("copilot should be listed when signed in")
	}
	if p.Account == nil || p.Account.User != "octo" || p.Account.Agent != "Copilot" {
		t.Fatalf("copilot account = %+v", p.Account)
	}
	ms := p.Available()
	if len(ms) == 0 {
		t.Fatal("copilot should offer its built-in list before any refresh")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Fatal("copilot available models must not include auto")
		}
	}
}

func TestL4AcctTCopilotChatFilter(t *testing.T) {
	yes, no := true, false
	enabled := &struct {
		State string `json:"state"`
	}{State: "enabled"}
	disabled := &struct {
		State string `json:"state"`
	}{State: "disabled"}
	cases := []struct {
		name string
		row  l4CopilotModelRow
		want bool
	}{
		{"chat ok", l4CopilotModelRow{ID: "gpt-4.1", PickerEnabled: &yes, Policy: enabled}, true},
		{"chat type ok", l4CopilotModelRow{ID: "gpt-4.1", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}}, true},
		{"auto", l4CopilotModelRow{ID: "auto"}, false},
		{"embedding by type", l4CopilotModelRow{ID: "text-embedding-3", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "embeddings"}}, false},
		{"embedding by id", l4CopilotModelRow{ID: "some-embed-model"}, false},
		{"internal", l4CopilotModelRow{ID: "internal-router"}, false},
		{"not picker enabled", l4CopilotModelRow{ID: "gpt-x", PickerEnabled: &no}, false},
		{"policy disabled", l4CopilotModelRow{ID: "gpt-y", Policy: disabled}, false},
	}
	for _, c := range cases {
		if got := l4CopilotChatModel(c.row); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestL4AcctTFetchCopilotModelsSavesFilteredList(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCopilotLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/copilot_internal/v2/token"):
			if r.Header.Get("Authorization") != "token gho_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"token":"sess","expires_at":4102444800,"endpoints":{"api":"` + "http://" + r.Host + `/api"}}`))
		case strings.HasSuffix(r.URL.Path, "/api/models"):
			w.Write([]byte(`{"data":[
				{"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
				{"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
				{"id":"text-embedding-3-small","name":"Embed","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
				{"id":"off-model","name":"Off","model_picker_enabled":false,"capabilities":{"type":"chat"}}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	ms, err := FetchCopilotModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-4.1" {
		t.Fatalf("filtered list = %+v", ms)
	}
	p := l4AcctTFind(All(), CopilotID)
	if got := p.Available(); len(got) != 1 || got[0].ID != "gpt-4.1" {
		t.Fatalf("after refresh available = %+v", got)
	}
}

func TestL4AcctTCopilotSessionFailureAsksReLogin(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCopilotLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	t.Cleanup(func() { CopilotTokenURL = old })

	_, err := CopilotToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v", err)
	}
}

func TestL4AcctTCodexDefaultListHasGPT55(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCodexAuth(t, "chatgpt")
	p := l4AcctTFind(All(), CodexID)
	if p == nil {
		t.Fatal("codex not listed")
	}
	if !l4AcctTHasModel(p.Exposed(), "gpt-5.5") {
		t.Fatalf("codex default exposed list lacks gpt-5.5: %+v", p.Exposed())
	}
}

func TestL4AcctTCodexRefreshWritesBackAndKeepsFields(t *testing.T) {
	l4AcctTEnv(t)
	authPath := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	expired := l4AcctTJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	idTok := l4AcctTJWT(t, map[string]any{"email": "dev@example.com"})
	l4AcctTWrite(t, authPath, `{"auth_mode":"chatgpt","custom_field":"keep-me","tokens":{"id_token":"`+idTok+
		`","access_token":"`+expired+`","refresh_token":"rt-old","account_id":"acct-9"}}`)

	newAcc := l4AcctTJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "rt-old" || in["client_id"] != CodexClientID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"access_token":"` + newAcc + `","refresh_token":"rt-new"}`))
	}))
	defer srv.Close()
	oldURL := CodexTokenURL
	CodexTokenURL = srv.URL
	t.Cleanup(func() { CodexTokenURL = oldURL })

	creds, err := CodexToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != newAcc || creds.AccountID != "acct-9" || creds.Email != "dev@example.com" {
		t.Fatalf("creds = %+v", creds)
	}
	raw, _ := os.ReadFile(authPath)
	var doc struct {
		CustomField string `json:"custom_field"`
		Tokens      struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.CustomField != "keep-me" || doc.Tokens.AccessToken != newAcc || doc.Tokens.RefreshToken != "rt-new" || doc.Tokens.AccountID != "acct-9" {
		t.Fatalf("auth.json after refresh = %s", raw)
	}
}

func TestL4AcctTCodexRefreshFailureSaysSignedOut(t *testing.T) {
	l4AcctTEnv(t)
	authPath := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	expired := l4AcctTJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	l4AcctTWrite(t, authPath, `{"auth_mode":"chatgpt","tokens":{"access_token":"`+expired+`","refresh_token":"rt-dead"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	oldURL := CodexTokenURL
	CodexTokenURL = srv.URL
	t.Cleanup(func() { CodexTokenURL = oldURL })

	_, err := CodexToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已登出，请运行 `codex login`") {
		t.Fatalf("err = %v", err)
	}
}

func TestL4AcctTJWTClaims(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := l4AcctTJWT(t, map[string]any{"exp": exp.Unix(), "email": "a@b.c"})
	c, err := l4JwtClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if l4StringClaim(c, "email") != "a@b.c" {
		t.Fatalf("email = %v", c["email"])
	}
	got, ok := l4JwtExpiry(tok)
	if !ok || !got.Equal(exp) {
		t.Fatalf("expiry = %v, %v", got, ok)
	}
	if _, err := l4JwtClaims("not-a-jwt"); err == nil {
		t.Fatal("expected error for a non-JWT")
	}
}
