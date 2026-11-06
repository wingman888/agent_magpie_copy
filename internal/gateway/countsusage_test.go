package gateway

import "testing"

// A request an agent sends to a routing group is not counted in usage.
func TestRgxCountsUsageSkipsGroups(t *testing.T) {
	if countsUsage("group/mine") || countsUsage("group/auto-m") {
		t.Fatal("a routing group's request should not be counted")
	}
	if !countsUsage("a/m") || !countsUsage("m") {
		t.Fatal("a provider's request should be counted")
	}
}
