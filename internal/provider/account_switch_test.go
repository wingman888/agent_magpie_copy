package provider

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

func modelIDs(ms []catalog.Model) string {
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ",")
}

// A refresh writes the login's own key and the plain "copilot" key; reads go
// only through the login's own key, so a switch or sign-out never reuses it.
func TestCopilotSwitchAndSignOutDoNotReuseRefreshedList(t *testing.T) {
	home := isolate(t)
	appsPath := copilotAppsPathFor(home)
	writeFile(t, appsPath, `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, 0)

	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := catalog.Live("copilot-octo"); !ok {
		t.Fatal("login-bound list not saved")
	}
	if _, _, ok := catalog.Live("copilot"); !ok {
		t.Fatal("plain copilot list not saved")
	}
	p, _ = Find("copilot")
	if got := modelIDs(p.Available()); got != "gpt-4.1,gemini-2.5-pro" {
		t.Fatalf("octo Available = %q", got)
	}
	builtin := modelIDs(copilotBuiltin())

	// switch to another GitHub login: it must see the built-in list
	writeFile(t, appsPath, `{"github.com:Iv1.xyz":{"user":"bob","oauth_token":"gho_TOKEN"}}`)
	p, _ = Find("copilot")
	if got := modelIDs(p.Available()); got != builtin {
		t.Errorf("after switching login Available = %q, want built-in %q", got, builtin)
	}
	if _, ok := p.Fetched(); ok {
		t.Errorf("after switching login Fetched reports octo's refresh")
	}

	// sign out: the provider disappears, and a stray plain key is not read
	if err := os.Remove(appsPath); err != nil {
		t.Fatal(err)
	}
	if hasID(All(), "copilot") {
		t.Fatal("copilot still listed after sign-out")
	}
	if got := modelIDs(accountAvailable(Provider{ID: CopilotID})); got != builtin {
		t.Errorf("signed-out accountAvailable = %q, want built-in %q", got, builtin)
	}
}
