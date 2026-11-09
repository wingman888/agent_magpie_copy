package agent

import "testing"

// The gemini agent has a field named "provider" (labelled "auth" in the
// picker). The spec requires both names to reach the same field.
func TestS5GeminiHasProviderField(t *testing.T) {
	a, _ := gmTEnv(t)
	f := a.Field("provider")
	if f == nil {
		t.Fatal("gemini has no field named provider")
	}
	if f.Key != "provider" || f.Label != "auth" {
		t.Errorf("provider field key/label = %q/%q, want provider/auth", f.Key, f.Label)
	}
	if a.Field("auth") != f {
		t.Error("label auth should find the same field as key provider")
	}
	if a.Field("model") == nil {
		t.Error("gemini model field missing")
	}
}
