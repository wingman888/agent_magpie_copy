package gateway

import "testing"

// Only explicit status codes and quota or rate-limit wording make an error
// retryable; a number or a plain "insufficient" in a 400 does not.
func TestS6RetryTextIsExplicit(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want bool
	}{
		{"status 429", true},
		{"HTTP 503 from upstream", true},
		{"error 500: internal", true},
		{"429 Too Many Requests", true},
		{"rate limit reached", true},
		{"insufficient_quota", true},
		{"insufficient balance", true},
		{"Service Unavailable", true},
		{"额度已用完", true},
		{"max_tokens 5000 is too large", false},
		{"expected 500 tokens, got 3", false},
		{"invalid request: field 402 missing", false},
		{"insufficient parameters for tool call", false},
		{"balance of the parameter list is wrong", false},
		{"bad request", false},
		{"", false},
	} {
		if got := rsiRetryText(c.msg); got != c.want {
			t.Errorf("retryText(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestS6RetryableStatuses(t *testing.T) {
	for _, c := range []struct {
		status int
		msg    string
		want   bool
	}{
		{400, "insufficient balance in request body", false},
		{400, "max_tokens 500", false},
		{402, "", true},
		{403, "额度已用完", true},
		{403, "forbidden", false},
	} {
		if got := rsiRetryable(c.status, c.msg); got != c.want {
			t.Errorf("retryable(%d, %q) = %v, want %v", c.status, c.msg, got, c.want)
		}
	}
}
