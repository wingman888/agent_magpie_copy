package catalog

import (
	"reflect"
	"testing"
)

func TestLiveKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := SaveLive("apis-roundtrip", "https://x.test", []Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := Live("apis-roundtrip")
	if !ok || len(ms) != 2 {
		t.Fatalf("live: %v %+v", ok, ms)
	}
	if !reflect.DeepEqual(ms[0].APIs, []string{"responses"}) {
		t.Fatalf("apis lost: %+v", ms[0])
	}
	if ms[1].APIs != nil {
		t.Fatalf("undeclared apis should stay nil: %+v", ms[1])
	}
}
