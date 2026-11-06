package provider

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

// Picks of an account provider are kept only while the account offers them.
func TestAccountPicksFollowWhatTheAccountOffers(t *testing.T) {
	home := setupHome(t)
	signInCopilot(t, home)
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
	if got := ids(p.Exposed()); !sameIDs(got, []string{"gpt-4.1"}) {
		t.Fatalf("exposed = %v, want only the picks the account offers", got)
	}
	for _, e := range Catalog() {
		if e.ID == "copilot/auto" || e.ID == "copilot/retired-model" {
			t.Fatalf("%s reached the catalog", e.ID)
		}
	}
}

// An account provider with no picks exposes everything it offers, uncapped.
func TestAccountWithoutPicksIsNotCapped(t *testing.T) {
	home := setupHome(t)
	signInCopilot(t, home)
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
func TestCopilotAutoNeverExposed(t *testing.T) {
	home := setupHome(t)
	signInCopilot(t, home)
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
func TestRefusedCopilotSessionSaysSignIn(t *testing.T) {
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
