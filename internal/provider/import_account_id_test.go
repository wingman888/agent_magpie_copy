package provider

import "testing"

// A link may not take the id of a signed-in agent account, whether the id is
// given outright or derived from the name.
func TestImportRejectsAccountIDs(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=Claude&chat=https://api.x.com/v1",
		"magpie://import?name=Codex&chat=https://api.x.com/v1",
		"magpie://import?name=Something&id=copilot&chat=https://api.x.com/v1",
		"magpie://import?name=Something&id=claude&chat=https://api.x.com/v1",
	} {
		p, err := ParseImport(link)
		if err == nil {
			t.Errorf("%s accepted as %+v", link, p)
		}
	}
	// a near miss is still fine
	if _, err := ParseImport("magpie://import?name=Claude%20Relay&chat=https://api.x.com/v1"); err != nil {
		t.Errorf("claude-relay rejected: %v", err)
	}
}
