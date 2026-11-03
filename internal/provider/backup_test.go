package provider

import "testing"

// A backup may not bring in a provider named "group": Save refuses that id,
// so Restore does too, and stores nothing from the backup when it does.
func TestF09RestoreRefusesGroupProviderID(t *testing.T) {
	f09GroupHome(t)
	ps := []Provider{
		{ID: "fine", Name: "Fine", Chat: "https://fine.test/v1", Key: "k"},
		{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "k"},
	}
	added, replaced, err := Restore(ps)
	if err == nil {
		t.Fatal(`provider id "group" restored`)
	}
	if added != 0 || replaced != 0 {
		t.Errorf("counts on refusal: added %d, replaced %d", added, replaced)
	}
	for _, p := range Stored() {
		if p.ID == "group" || p.ID == "fine" {
			t.Errorf("stored %q after a refused restore", p.ID)
		}
	}
}

// Restore of valid providers still works, and its counts are as before.
func TestF09RestoreStillTakesValidProviders(t *testing.T) {
	f09GroupHome(t)
	added, replaced, err := Restore([]Provider{{ID: "fine", Name: "Fine", Chat: "https://fine.test/v1", Key: "k"}})
	if err != nil || added != 1 || replaced != 0 {
		t.Fatalf("restore: added %d, replaced %d, err %v", added, replaced, err)
	}
	if _, err := Find("fine"); err != nil {
		t.Fatalf("restored provider not found: %v", err)
	}
}
