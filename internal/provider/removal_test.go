package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeClaudeStatus makes `claude auth status --json` print out and exit with
// code. A real non-zero exit is used so the stdout-with-error path is exercised.
func fakeClaudeStatus(t *testing.T, out string, code int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh to fake the claude CLI")
	}
	claudeAuthStatusRun = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1"; exit "$2"`, "sh", out, strconv.Itoa(code)).Output()
	}
	forgetClaudeCredential()
}

func TestRemovedAccountKeepsPicksAndComesBack(t *testing.T) {
	home := signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	login, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still in All()")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("removed codex still in catalog: %s", e.ID)
		}
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves")
	}
	if x := Excluded(); len(x) != 1 || x[0].Agent != "codex" || x[0].Provider != "codex" || !strings.Contains(x[0].Why, "magpie provider add codex") {
		t.Fatalf("excluded: %+v", x)
	}
	// the agent's own sign-in is not touched
	if again, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json")); string(again) != string(login) {
		t.Fatal("codex login was modified by removal")
	}
	// the picks are kept on disk for when the account comes back
	if b, _ := os.ReadFile(Path()); !strings.Contains(string(b), `"gpt-5.5"`) {
		t.Fatalf("picks dropped on removal: %s", b)
	}

	// `magpie provider add codex` brings it back with its model choice
	if err := Save(Provider{ID: "codex", Name: "codex"}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "codex")
	if !ok || p.Account == nil || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("restored codex: %+v", p)
	}
	if x := Excluded(); len(x) != 0 {
		t.Fatalf("still excluded after add: %+v", x)
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); !ok {
		t.Fatal("codex/gpt-5.5 does not resolve after add")
	}
}

func TestRemovedClaudeIsHiddenAndComesBack(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))
	fakeClaudeStatus(t, `{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, 0)

	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude not listed before removal")
	}
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("removed claude still listed")
	}
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("claude credentials touched: %v", err)
	}
	if x := Excluded(); len(x) != 1 || x[0].Agent != "claude" {
		t.Fatalf("excluded: %+v", x)
	}
	if err := Save(Provider{ID: "claude", Name: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude not back after add")
	}
}

func TestClaudeSignedOutIsNotAProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// credentials remain, but Claude Code says it is signed out: the exit code
	// is non-zero and the JSON still says so
	fakeClaudeStatus(t, `{"loggedIn":false}`, 1)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out claude (exit 1) listed as a provider")
	}

	fakeClaudeStatus(t, `{"loggedIn":false}`, 0)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out claude (exit 0) listed as a provider")
	}
}

func TestClaudeStatusKeepsJudgmentWhenUnknown(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// no claude command at all
	claudeAuthStatusRun = func(context.Context) ([]byte, error) { return nil, exec.ErrNotFound }
	forgetClaudeCredential()
	if p, ok := find(All(), "claude"); !ok || p.Account == nil || p.Account.Plan != "max" {
		t.Fatalf("missing CLI: %+v %v", p, ok)
	}

	// output that is not a status answer, from a clean exit or a failed one
	fakeClaudeStatus(t, `not json`, 0)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("unparseable output (exit 0) dropped the provider")
	}
	fakeClaudeStatus(t, `error: something`, 1)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("unparseable output (exit 1) dropped the provider")
	}
	fakeClaudeStatus(t, `{"email":"x@example.com"}`, 0)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("status without loggedIn dropped the provider")
	}
}

func TestClaudeStatusLoggedInReadsIdentity(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	fakeClaudeStatus(t, `{"loggedIn":true,"email":" me@example.com ","subscriptionType":"pro"}`, 0)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("logged in: %+v %v", p, ok)
	}
}
