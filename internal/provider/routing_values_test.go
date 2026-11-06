package provider

import "testing"

// A provider's routing takes order, rotate and least-used; the "usage" an
// older config saved is least-used; anything else is smart.
func TestRoutingValues(t *testing.T) {
	groupSetup(t)
	cases := map[string]string{
		"order":      "order",
		"rotate":     "rotate",
		"least-used": "least-used",
		"usage":      "least-used",
		"ordered":    "",
		"bogus":      "",
	}
	for in, want := range cases {
		// a group takes only the names it is written with: "usage" is not one
		groupWant := want
		if in == "usage" {
			groupWant = ""
		}
		if err := SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: in}); err != nil {
			t.Fatal(err)
		}
		if g, _, _ := FindGroup("r"); g.Routing != groupWant {
			t.Errorf("group routing %q: got %q, want %q", in, g.Routing, groupWant)
		}
		if err := Save(Provider{ID: "a", Name: "A", Key: "k", Chat: "https://a.example/v1", Routing: in}); err != nil {
			t.Fatal(err)
		}
		if p, err := Find("a"); err != nil || p.Routing != want {
			t.Errorf("provider routing %q: got %q (%v), want %q", in, p.Routing, err, want)
		}
	}
}
