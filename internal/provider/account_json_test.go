package provider

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// jsonKeys returns the sorted keys of a JSON object.
func jsonKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %s: %v", b, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sameKeys(got []string, want ...string) bool {
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// forbiddenTokenWords must never appear in any JSON this package emits
// for account providers or exclusions.
var forbiddenTokenWords = []string{"access_token", "oauth_token", "refresh_token", "token", "ACCESS-SECRET"}

func assertNoTokenWords(t *testing.T, b []byte) {
	t.Helper()
	s := string(b)
	for _, w := range forbiddenTokenWords {
		if strings.Contains(s, w) {
			t.Fatalf("JSON leaks %q: %s", w, s)
		}
	}
}

func TestAccountJSONKeys(t *testing.T) {
	b, err := json.Marshal(Account{Agent: "codex", User: "dev@example.com", Plan: "pro"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameKeys(jsonKeys(t, b), "agent", "user", "plan") {
		t.Fatalf("Account keys = %v, want agent,user,plan: %s", jsonKeys(t, b), b)
	}
	assertNoTokenWords(t, b)

	// plan is optional: omitted when empty.
	b, _ = json.Marshal(Account{Agent: "copilot", User: "octocat"})
	if !sameKeys(jsonKeys(t, b), "agent", "user") {
		t.Fatalf("Account without plan keys = %v, want agent,user: %s", jsonKeys(t, b), b)
	}
}

func TestExclusionJSONKeys(t *testing.T) {
	b, err := json.Marshal(Exclusion{Agent: "claude", Why: "terms"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameKeys(jsonKeys(t, b), "agent", "why") {
		t.Fatalf("Exclusion keys = %v, want agent,why: %s", jsonKeys(t, b), b)
	}
	assertNoTokenWords(t, b)
}

func TestAccountProviderJSONShape(t *testing.T) {
	home := setHome(t)
	writeCodexLogin(t, home)

	p, ok := findProv(t, Accounts(), "codex")
	if !ok {
		t.Fatal("codex account missing")
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	keys := jsonKeys(t, b)
	// Same key layout as an ordinary provider, plus "account".
	for _, want := range []string{"id", "name", "key", "account"} {
		found := false
		for _, k := range keys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("account provider JSON lacks %q: %s", want, b)
		}
	}
	var m struct {
		ID      string   `json:"id"`
		Key     string   `json:"key"`
		Models  []string `json:"models"`
		Account Account  `json:"account"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "codex" || m.Key != "" {
		t.Fatalf("id/key = %q/%q, want codex/empty", m.ID, m.Key)
	}
	if m.Account.Agent != "codex" || m.Account.User != "dev@example.com" || m.Account.Plan != "pro" {
		t.Fatalf("account = %+v", m.Account)
	}
	if len(m.Models) != 0 {
		t.Fatalf("models should be omitted when no selection: %s", b)
	}
	assertNoTokenWords(t, b)
	t.Logf("codex provider JSON: %s", b)
}

func TestExcludedClaudeJSONHasNoCredentials(t *testing.T) {
	home := setHome(t)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"CLAUDE-SECRET","refreshToken":"CLAUDE-REFRESH"}}`)
	ex := Excluded()
	if len(ex) == 0 {
		t.Fatal("claude sign-in should be reported as excluded")
	}
	b, err := json.Marshal(ex)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if len(r) != 2 {
			t.Fatalf("exclusion has extra keys: %s", b)
		}
		if _, ok := r["agent"]; !ok {
			t.Fatalf("exclusion lacks agent: %s", b)
		}
		if _, ok := r["why"]; !ok {
			t.Fatalf("exclusion lacks why: %s", b)
		}
	}
	assertNoTokenWords(t, b)
	if strings.Contains(string(b), "CLAUDE-SECRET") || strings.Contains(string(b), "CLAUDE-REFRESH") {
		t.Fatalf("claude credentials leaked: %s", b)
	}
}
