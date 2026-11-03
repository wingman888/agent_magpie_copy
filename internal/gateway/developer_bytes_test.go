package gateway

import "testing"

// Only the role values may change; everything else, including whitespace,
// escapes and number spelling, must come back exactly as sent.
func TestDeveloperToSystemKeepsOtherBytes(t *testing.T) {
	in := `{ "model" : "m" ,"stream":true,"messages" : [ {"role":"developer","content":"x<<&"} , {"role" : "user", "content":"hi"} ],"extra":1.50e3 }`
	want := `{ "model" : "m" ,"stream":true,"messages" : [ {"role":"system","content":"x<<&"} , {"role" : "user", "content":"hi"} ],"extra":1.50e3 }`
	if got := string(developerToSystem([]byte(in))); got != want {
		t.Errorf("bytes changed beyond the role:\n got %s\nwant %s", got, want)
	}
}

func TestDeveloperToSystemEscapedRoleAndRepeats(t *testing.T) {
	in := `{"messages":[{"role":"developer","content":"a"},{"content":"no role"},{"role":"developer","content":"b"}]}`
	want := `{"messages":[{"role":"system","content":"a"},{"content":"no role"},{"role":"system","content":"b"}]}`
	if got := string(developerToSystem([]byte(in))); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestDeveloperToSystemIgnoresContentText(t *testing.T) {
	in := `{"messages":[{"role":"user","content":"developer"},{"role":"developer","developer":"role"}]}`
	want := `{"messages":[{"role":"user","content":"developer"},{"role":"system","developer":"role"}]}`
	if got := string(developerToSystem([]byte(in))); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
