package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func grokSignedIn(t *testing.T, home, email string) {
	t.Helper()
	writeFile(t, filepath.Join(home, "auth.json"), map[string]any{
		"https://auth.x.ai": map[string]any{"key": "k-" + email, "email": email, "expires_at": time.Now().Add(time.Hour)},
	})
}

// A further Grok account signs in in a home of magpie's: the CLI's own
// stays as it is, both are in use, and either can go first.
func TestGrokAccounts(t *testing.T) {
	home := signIn(t)
	own := filepath.Join(home, ".grok")
	grokSignedIn(t, own, "me@x.ai")

	extra, err := newGrokHome()
	if err != nil {
		t.Fatal(err)
	}
	grokSignedIn(t, extra, "two@x.ai")
	if u, err := addGrokLogin(extra); err != nil || u != "two@x.ai" {
		t.Fatalf("add: %q %v", u, err)
	}
	ls := Logins("grok")
	if len(ls) != 2 || ls[0].User != "me@x.ai" || !ls[0].Active || ls[1].User != "two@x.ai" || !ls[1].On {
		t.Fatalf("logins: %+v", ls)
	}
	p := Provider{ID: "grok", Account: &Account{Agent: "grok", User: "me@x.ai"}}
	if also := p.AlsoOn(); len(also) != 1 || also[0].Account.User != "two@x.ai" || also[0].Account.Home != extra {
		t.Fatalf("also on: %+v", also)
	}

	if err := SwitchLogin("grok", "two@x.ai"); err != nil {
		t.Fatal(err)
	}
	ls = Logins("grok")
	if ls[0].User != "two@x.ai" || !ls[0].Active || !ls[1].On {
		t.Fatalf("after switch: %+v", ls)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "auth.json")); len(b) == 0 {
		t.Fatal("the CLI's own sign-in was touched")
	}
	if err := ForgetLogin("grok", "two@x.ai"); err == nil {
		t.Fatal("forgot the account in use first")
	}
	if err := ForgetLogin("grok", "me@x.ai"); err == nil {
		t.Fatal("forgot the CLI's own sign-in")
	}
	if err := SetLoginOn("grok", "me@x.ai", false); err != nil {
		t.Fatal(err)
	}
	if also := p.AlsoOn(); len(also) != 0 {
		t.Fatalf("turned off, still on: %+v", also)
	}

	// signed in again, an account keeps one home
	again, _ := newGrokHome()
	grokSignedIn(t, again, "two@x.ai")
	addGrokLogin(again)
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Fatal("the old home was kept")
	}
	SwitchLogin("grok", "me@x.ai")
	if err := ForgetLogin("grok", "two@x.ai"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(again); !os.IsNotExist(err) || len(Logins("grok")) != 1 {
		t.Fatalf("forgotten: %v %+v", err, Logins("grok"))
	}
}
