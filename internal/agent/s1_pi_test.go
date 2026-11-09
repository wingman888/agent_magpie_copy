package agent

import (
	"reflect"
	"testing"
)

func TestS1PiThinkingLevelMap(t *testing.T) {
	cases := []struct {
		efforts []string
		want    map[string]string
	}{
		{[]string{"low", "medium", "high"}, nil},
		{nil, nil},
		{[]string{"low", "medium", "high", "max"}, map[string]string{"max": "max"}},
		{[]string{"low", "medium", "high", "xhigh"}, map[string]string{"xhigh": "xhigh"}},
		{[]string{"minimal", "low", "high", "xhigh", "max"}, map[string]string{"xhigh": "xhigh", "max": "max"}},
	}
	for _, c := range cases {
		if got := piThinkingLevels(c.efforts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("piThinkingLevels(%v) = %v, want %v", c.efforts, got, c.want)
		}
	}
}
