package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tomlDoc = `model = "gpt-6-astra"   # keep
approval_policy = "never"

[notice]
hide = true

[projects."/x"]
trust_level = "trusted"
`

func tmpToml(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(tomlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTOMLTables(t *testing.T) {
	p := tmpToml(t)
	got := strings.Join(TOMLTables(p), ",")
	if got != `notice,projects."/x"` {
		t.Fatalf("tables = %q", got)
	}
}

func TestSetAndDelTOMLTable(t *testing.T) {
	p := tmpToml(t)
	if err := SetTOMLTable(p, "model_providers.deepseek", KV{Path: "name", Value: "DeepSeek"}, KV{Path: "wire_api", Value: "responses"}); err != nil {
		t.Fatal(err)
	}
	kv := GetTOMLTable(p, "model_providers.deepseek")
	if kv["name"] != "DeepSeek" || kv["wire_api"] != "responses" {
		t.Fatalf("table = %v", kv)
	}
	// replace in place, other tables intact
	if err := SetTOMLTable(p, "model_providers.deepseek", KV{Path: "name", Value: "DS"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Count(s, "[model_providers.deepseek]") != 1 || strings.Contains(s, "wire_api") || !strings.Contains(s, "trust_level") || !strings.Contains(s, "# keep") {
		t.Fatalf("after replace:\n%s", s)
	}
	if err := DelTOMLTable(p, "model_providers.deepseek"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if string(raw) != tomlDoc {
		t.Fatalf("delete did not restore:\n%s", raw)
	}
}

func TestSetTOMLTableMiddle(t *testing.T) {
	p := tmpToml(t)
	if err := SetTOMLTable(p, "notice", KV{Path: "hide", Value: false}, KV{Path: "n", Value: 3}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	want := "[notice]\nhide = false\nn = 3\n\n[projects.\"/x\"]"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("got:\n%s", raw)
	}
}

func TestDelTOMLTop(t *testing.T) {
	p := tmpToml(t)
	if err := DelTOMLTop(p, "model", "hide"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Contains(s, "gpt-6-astra") || !strings.Contains(s, "hide = true") || !strings.HasPrefix(s, "approval_policy") {
		t.Fatalf("got:\n%s", s)
	}
}
