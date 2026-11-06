package provider

import (
	"os"
	"strings"
	"testing"
)

// Rule under test: whether a provider is an account is decided by the agent's
// sign-in alone. A request body that carries an Account (or a key, address,
// and so on) must not make a provider an account, and nothing it sends may be
// written to dial's file beyond the id and the chosen models.

func f02ReadStore(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestF02SaveAccountClaimIgnoredWhenSignedIn(t *testing.T) {
	home := f02SetHome(t)
	f02WriteCodexLogin(t, home)
	err := Save(Provider{
		ID:      "codex",
		Account: &Account{Agent: "codex", User: "x"},
		Key:     "sk-x",
		Chat:    "http://evil",
		Models:  []string{"gpt-5.5"},
	})
	if err != nil {
		t.Fatalf("save while signed in: %v", err)
	}
	s := f02ReadStore(t)
	for _, bad := range []string{"sk-x", "evil", `"account"`, `"key"`, `"chat"`} {
		if strings.Contains(s, bad) {
			t.Fatalf("store must not contain %q: %s", bad, s)
		}
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || p.Account.User != "dev@example.com" {
		t.Fatalf("account identity must come from the login, got %+v", p.Account)
	}
	if p.Key != "" || p.Chat != "" {
		t.Fatalf("account provider must not carry key/address: key=%q chat=%q", p.Key, p.Chat)
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("only the chosen models may be exposed, got %v", p.Models)
	}
}

func TestF02SaveAccountClaimWithoutSignInIsRefused(t *testing.T) {
	f02SetHome(t)
	err := Save(Provider{ID: "codex", Account: &Account{Agent: "codex", User: "x"}, Models: []string{"gpt-5.5"}})
	if err == nil {
		t.Fatal("a claimed account with no sign-in and no address must be refused")
	}
	if _, statErr := os.Stat(Path()); statErr == nil {
		t.Fatal("nothing may be written for a refused account save")
	}
	if len(All()) != 0 {
		t.Fatalf("no provider may appear from a claim: %+v", All())
	}
}

func TestF02SaveClaimWithAddressWithoutSignInIsUsersOwn(t *testing.T) {
	f02SetHome(t)
	err := Save(Provider{ID: "codex", Account: &Account{Agent: "codex", User: "x"},
		Key: "sk-x", Chat: "http://localhost:9999/v1"})
	if err != nil {
		t.Fatalf("with an address it is the user's own provider: %v", err)
	}
	s := f02ReadStore(t)
	if strings.Contains(s, `"account"`) {
		t.Fatalf("request's account must not be written: %s", s)
	}
	ps := All()
	p, ok := f02FindProv(t, ps, "codex")
	if !ok || p.Account != nil || p.Key != "sk-x" {
		t.Fatalf("expected the user's own codex provider, got %+v", p)
	}
}

func TestF02PlantedAccountFieldOnDiskIsIgnored(t *testing.T) {
	f02SetHome(t)
	// a hand-edited file that claims an account for a user-built provider
	f02WriteFile(t, Path(), `{"providers":[{"id":"copilot","chat":"http://localhost:9999/v1","key":"k","account":{"agent":"copilot","user":"evil"}}]}`)
	p, ok := f02FindProv(t, All(), "copilot")
	if !ok || p.Account != nil || p.Chat == "" {
		t.Fatalf("planted account must not make a provider an account: %+v", p)
	}
	if len(Accounts()) != 0 {
		t.Fatal("no account provider may come from a planted field")
	}
	// a planted account with no address and no sign-in is not a provider
	f02WriteFile(t, Path(), `{"providers":[{"id":"copilot","account":{"agent":"copilot","user":"evil"},"models":["x"]}]}`)
	if _, ok := f02FindProv(t, All(), "copilot"); ok {
		t.Fatal("a planted account with no sign-in must not be listed")
	}
}

func TestF02DeleteAccountKeepsProviderWhenSignedIn(t *testing.T) {
	home := f02SetHome(t)
	f02WriteCodexLogin(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok := f02FindProv(t, All(), "codex")
	if !ok || len(p.Models) != 0 || p.Account == nil {
		t.Fatalf("delete must only clear the selection: ok=%v %+v", ok, p)
	}
}
