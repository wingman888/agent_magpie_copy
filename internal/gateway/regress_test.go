package gateway

import "testing"

// A Claude Code failure moves on to another account only when its text names a
// limit, an overload or an outage; a rejected request stays put.
func TestClaudeTransientOnlyLimitsAndOutages(t *testing.T) {
	for _, msg := range []string{
		"Claude Code: API Error: 429 rate_limit_error",
		"Claude Code: Claude AI usage limit reached",
		"Claude Code: API Error: 529 overloaded_error",
		"Claude Code: 503 service unavailable",
	} {
		if !claudeTransient(msg) {
			t.Errorf("%q should move on", msg)
		}
	}
	for _, msg := range []string{
		"Claude Code: API Error: 400 invalid_request_error: messages: field required",
		"Claude Code is not installed; install it and run `claude auth login`",
	} {
		if claudeTransient(msg) {
			t.Errorf("%q must not move on", msg)
		}
	}
}
