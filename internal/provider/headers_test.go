package provider

import (
	"maps"
	"os"
	"strings"
	"testing"
)

func TestSaveCleansHeaders(t *testing.T) {
	isolateIcons(t)
	err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k",
		Headers: map[string]string{
			"  X-Org-Id  ": "  acme  ",
			"X-ORG-id":     "upper",
			"":             "dropped",
			"   ":          "also dropped",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-ORG-id": "upper"}
	if !maps.Equal(p.Headers, want) {
		t.Errorf("Headers = %v, want %v", p.Headers, want)
	}
}

func TestEmptyHeadersNotWritten(t *testing.T) {
	isolateIcons(t)
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "https://bare.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"headers"`) {
		t.Errorf("providers.json contains a headers field for a provider without headers:\n%s", b)
	}
}

func TestHeadersRoundTrip(t *testing.T) {
	isolateIcons(t)
	if err := Save(Provider{
		ID: "rt", Name: "RT", Chat: "https://rt.example/v1", Key: "k",
		Headers: map[string]string{"X-Tenant": "t1"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"headers": {`) || !strings.Contains(string(b), `"X-Tenant": "t1"`) {
		t.Errorf("providers.json lacks the headers object:\n%s", b)
	}
}

func TestPresetDropsHeaders(t *testing.T) {
	isolateIcons(t)
	pr := Presets()[0]
	if err := Save(Provider{
		ID: pr.ID, Name: pr.Name, Preset: pr.ID, Key: "k",
		Chat:    "https://preset.example/v1",
		Headers: map[string]string{"X-Org-Id": "acme"},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := Find(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 0 {
		t.Errorf("preset kept headers: %v", p.Headers)
	}
}
