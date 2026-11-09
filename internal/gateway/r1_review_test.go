package gateway

import "testing"

// A healthy stream whose content happens to say "quota" or carry an id that
// looks like an error code must not be taken for a start error.
func TestR1StartErrorIgnoresHealthyContent(t *testing.T) {
	healthy := []string{
		"data: {\"id\":\"req-429-quota\",\"choices\":[{\"delta\":{\"content\":\"your credit balance is fine\"}}]}\n\n",
		"data: {\"id\":\"x\",\"error\":null,\"note\":\"rate limit\"}\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_quota\"}}\n\n",
	}
	for _, raw := range healthy {
		if msg, ok := startError([]byte(raw)); ok {
			t.Fatalf("healthy event taken for a start error %q: %s", msg, raw)
		}
	}
}

// Only an error event, or a non-null top-level error, that names a retryable
// condition in its message counts.
func TestR1StartErrorMatchesOnlyTheMessage(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\n", true},
		{"event: error\ndata: {\"type\":\"error\",\"message\":\"insufficient credit\"}\n\n", true},
		{"data: {\"error\":\"quota used up\"}\n\n", true},
		{"data: {\"error\":{\"message\":\"bad parameter\"}}\n\n", false},
		{"event: error\ndata: {\"type\":\"error\",\"message\":\"invalid model\"}\n\n", false},
	}
	for _, c := range cases {
		if _, ok := startError([]byte(c.raw)); ok != c.want {
			t.Fatalf("startError(%q) = %v, want %v", c.raw, ok, c.want)
		}
	}
}
