package agent

import (
	"reflect"
	"testing"
)

func TestPiThinkingLevels(t *testing.T) {
	for _, c := range []struct {
		efforts []string
		want    map[string]any
	}{
		{[]string{"low", "high", "max"}, map[string]any{"max": "max"}},
		{[]string{"low", "medium", "high", "xhigh"}, map[string]any{"xhigh": "xhigh"}},
		{[]string{"none", "low", "high", "xhigh", "max"}, map[string]any{"xhigh": "xhigh", "max": "max"}},
		{[]string{"low", "medium", "high"}, nil},
		{nil, nil},
	} {
		if got := piThinkingLevels(c.efforts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.efforts, got, c.want)
		}
	}
}
