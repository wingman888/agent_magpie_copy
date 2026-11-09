package agent

import (
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

// TestFixCop: the built-in Copilot list is non-empty and never carries
// "auto", while the Copilot CLI picker still offers "auto" first.
func TestFixCop(t *testing.T) {
	list := catalog.Builtin("copilot")
	if len(list) == 0 {
		t.Fatal("copilot built-in list is empty")
	}
	for _, m := range list {
		if m.ID == "auto" {
			t.Fatalf("copilot built-in list contains auto: %v", list)
		}
	}
	for _, a := range All() {
		if a.ID != "copilot" {
			continue
		}
		opts := a.Fields[0].Options(nil)
		if len(opts) == 0 || opts[0].Value != "auto" {
			t.Fatalf("copilot CLI picker = %v, want auto first", opts)
		}
		if len(opts) != len(list)+1 {
			t.Fatalf("copilot CLI picker has %d options, want %d", len(opts), len(list)+1)
		}
	}
}
