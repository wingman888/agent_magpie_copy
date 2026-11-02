package gateway

import "testing"

// A run built without a bridge (as unit tests do) must survive its linger
// timer firing abort after the test has moved on.
func TestSubscriptionRunFinishWithoutBridge(t *testing.T) {
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}}
	run.abort()
	run.abort()
	var b *subscriptionBridge
	b.removeRun(run)
}
