package main

import "testing"

func TestLeavesAgentsAlone(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"ls"}, false},
		{[]string{"claude", "opus"}, false},
		{[]string{"restore", "b.magpie-backup"}, false},
		{[]string{"restore", "--no-agents", "b.magpie-backup"}, true},
		{[]string{"restore", "b.magpie-backup", "--no-agents"}, true},
		{[]string{"backup"}, true},
		{[]string{"backup", "--no-keys", "b.magpie-backup"}, true},
	}
	for _, c := range cases {
		if got := leavesAgentsAlone(c.args); got != c.want {
			t.Errorf("leavesAgentsAlone(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
