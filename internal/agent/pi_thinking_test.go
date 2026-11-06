package agent

import (
	"reflect"
	"testing"
)

func TestPiThinkingLevelMapL3x(t *testing.T) {
	cases := []struct {
		efforts []string
		want    map[string]string
	}{
		{[]string{"low", "medium", "high"}, nil},
		{nil, nil},
		{[]string{"low", "high", "max"}, map[string]string{"max": "max"}},
		{[]string{"minimal", "xhigh"}, map[string]string{"xhigh": "xhigh"}},
		{[]string{"high", "xhigh", "max"}, map[string]string{"max": "max", "xhigh": "xhigh"}},
	}
	for _, c := range cases {
		got := piThinkingLevelMapL3x(c.efforts)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("piThinkingLevelMapL3x(%v) = %v, want %v", c.efforts, got, c.want)
		}
	}
}
