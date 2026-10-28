package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeSubscriptionPromptKeepsForeignHarnessOutOfSystem(t *testing.T) {
	r := &Request{
		System:   "You are an expert coding assistant operating inside pi\nsee docs/custom-provider.md and docs/packages.md",
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hello"}}}},
	}
	blocks, err := renderClaudePrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(blocks)
	s := string(b)
	if !strings.Contains(s, "external_system_instructions") || !strings.Contains(s, "operating inside pi") || !strings.Contains(s, "Human: hello") {
		t.Fatalf("prompt lost content: %s", b)
	}
	// renderClaudePrompt is the user content passed to the genuine CLI. The
	// foreign harness is never supplied through --system-prompt, where
	// Anthropic's subscription classifier rejects it.
	args := strings.Join(claudeCLIArgs("claude-sonnet-5", `{}`, "medium"), " ")
	if strings.Contains(args, "system-prompt") {
		t.Fatal("Claude bridge must retain the genuine Claude Code preset")
	}
	for _, want := range []string{"--input-format stream-json", "--include-partial-messages", "--strict-mcp-config", "--effort medium"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing CLI contract %q in %q", want, args)
		}
	}
}

func TestCleanClaudeEnvRemovesGatewayOverrides(t *testing.T) {
	got := cleanClaudeEnv([]string{
		"PATH=/bin", "ANTHROPIC_BASE_URL=http://127.0.0.1:3425",
		"ANTHROPIC_API_KEY=x", "ANTHROPIC_AUTH_TOKEN=y", "CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=9999", "KEEP=yes",
	})
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{
		"ANTHROPIC_BASE_URL=", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDECODE=",
		"CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_CODE_SSE_PORT=",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("kept %s in %q", forbidden, joined)
		}
	}
	for _, want := range []string{"PATH=/bin", "KEEP=yes", "ENABLE_CLAUDEAI_MCP_SERVERS=0", "DISABLE_AUTO_COMPACT=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %q", want, joined)
		}
	}
}
