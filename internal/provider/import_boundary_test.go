package provider

import (
	"strings"
	"testing"
)

// Boundary cases from the import spec that the broader table does not name
// explicitly: a public IPv4 literal over plain http, and the exact 4096-char
// key limit (4096 accepted, 4097 rejected).
func TestImportBoundaryPublicIPAndKeyLimit(t *testing.T) {
	if _, err := ParseImport("magpie://import?name=X&chat=http://8.8.8.8/v1"); err == nil {
		t.Fatal("public IP over http accepted; want rejected")
	}
	if _, err := ParseImport("magpie://import?name=X&chat=http://127.0.0.1:8080"); err != nil {
		t.Fatalf("loopback http rejected: %v", err)
	}
	ok := "magpie://import?name=X&chat=https://a.example/v1&key=" + strings.Repeat("k", 4096)
	if _, err := ParseImport(ok); err != nil {
		t.Fatalf("4096-char key rejected: %v", err)
	}
	long := "magpie://import?name=X&chat=https://a.example/v1&key=" + strings.Repeat("k", 4097)
	if _, err := ParseImport(long); err == nil {
		t.Fatal("4097-char key accepted; want rejected")
	}
}
