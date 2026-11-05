package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestImportCLIConfirmYes(t *testing.T) {
	yes := []string{"y", "Y", "yes", "YES", "Yes", " y \n", "yes\r\n"}
	no := []string{"", "\n", "n", "N", "no", "yess", "ye", "sure", "0", "y n"}
	for _, s := range yes {
		if !confirmYes(s) {
			t.Errorf("confirmYes(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if confirmYes(s) {
			t.Errorf("confirmYes(%q) = true, want false", s)
		}
	}
}

func TestImportCLIIsImportLink(t *testing.T) {
	yes := []string{"magpie://import?preset=deepseek", "magpie:import?preset=deepseek", "MAGPIE://import/?x=1", "Magpie:///import?x=1"}
	no := []string{"import", "--help", "https://usemagpie.ai/import?x=1", "magpie", "", "xmagpie:import"}
	for _, s := range yes {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}

func TestImportCLISummaryHostsAndKey(t *testing.T) {
	p := provider.Provider{
		ID:        "acme",
		Name:      "Acme Relay",
		Chat:      "https://api.acme.example/v1",
		Anthropic: "https://api.acme.example",
		Responses: "https://responses.acme.example/v1",
		Key:       "sk-abcdefghijklmnop",
		Models:    []string{"gpt-5.5", "claude-sonnet-5"},
	}
	s := importSummary(p, false)
	for _, want := range []string{"Acme Relay", "acme", "api.acme.example", "responses.acme.example", "gpt-5.5, claude-sonnet-5", "Prompts and key will be sent to:"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "sk-abcdefghijklmnop") {
		t.Errorf("summary shows the full key:\n%s", s)
	}
	if strings.Contains(s, "replace") {
		t.Errorf("summary warns about replacing without an existing provider:\n%s", s)
	}
	// the host list must not repeat a host
	if n := strings.Count(s, "→ api.acme.example"); n != 2 {
		t.Errorf("expected both endpoints listed for api.acme.example, got %d:\n%s", n, s)
	}
}

func TestImportCLISummaryNoKey(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1"}
	s := importSummary(p, false)
	if !strings.Contains(s, "no key") {
		t.Errorf("summary does not say no key:\n%s", s)
	}
	if !strings.Contains(s, "Prompts will be sent to: relay.example") {
		t.Errorf("summary does not name the host that receives prompts:\n%s", s)
	}
}

func TestImportCLISummaryReplaceWarning(t *testing.T) {
	p := provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-123456789"}
	s := importSummary(p, true)
	if !strings.Contains(strings.ToLower(s), "replace") {
		t.Errorf("summary lacks replace warning:\n%s", s)
	}
	if !strings.Contains(s, "Warning: This will replace the existing provider deepseek") {
		t.Errorf("warning text unexpected:\n%s", s)
	}
	// the warning comes last, right before the question
	if !strings.HasSuffix(strings.TrimRight(s, "\n"), "provider deepseek") {
		t.Errorf("warning should be the last line of the summary:\n%s", s)
	}
}

func TestImportCLIMissingLinkIsUsage(t *testing.T) {
	err := importCmd(nil)
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("importCmd(nil) = %v, want usage error", err)
	}
	err = importCmd([]string{"-y"})
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("importCmd(-y) = %v, want usage error", err)
	}
}

func TestImportCLIRejectsUnknownFlagAndExtraArgs(t *testing.T) {
	if err := importCmd([]string{"--force", "magpie://import?preset=deepseek"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if err := importCmd([]string{"magpie://import?preset=deepseek", "magpie://import?preset=deepseek"}); err == nil {
		t.Error("two links accepted")
	}
}

func TestImportCLIRejectsBadLinkBeforeAnything(t *testing.T) {
	// ParseImport rejects these, so nothing is summarised, asked about or saved.
	for _, link := range []string{"https://usemagpie.ai/import?preset=deepseek", "magpie://delete?id=x", "magpie://import?name=magpie&chat=https://a.example/v1"} {
		if err := importCmd([]string{link}); err == nil {
			t.Errorf("importCmd(%q) accepted a bad link", link)
		}
	}
}
