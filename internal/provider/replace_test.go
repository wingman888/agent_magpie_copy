package provider

import "testing"

func TestKeyOnReplace(t *testing.T) {
	old := Provider{ID: "relay", Key: "sk-old", Chat: "https://relay.example.com/v1"}
	cases := []struct {
		name string
		next Provider
		key  string
		kept bool
	}{
		{"same host keeps key", Provider{ID: "relay", Chat: "https://relay.example.com/v2"}, "sk-old", true},
		{"other host drops key", Provider{ID: "relay", Chat: "https://other.example.org/v1"}, "", false},
		{"link key wins", Provider{ID: "relay", Key: "sk-new", Chat: "https://relay.example.com/v1"}, "sk-new", false},
		{"no hosts keeps nothing", Provider{ID: "relay"}, "", false},
	}
	for _, c := range cases {
		key, kept := KeyOnReplace(old, c.next)
		if key != c.key || kept != c.kept {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, key, kept, c.key, c.kept)
		}
	}
	if key, kept := KeyOnReplace(Provider{ID: "relay", Chat: "https://relay.example.com/v1"}, Provider{ID: "relay", Chat: "https://relay.example.com/v1"}); key != "" || kept {
		t.Errorf("old without key: got (%q, %v)", key, kept)
	}
}
