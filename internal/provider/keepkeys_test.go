package provider

import "testing"

// A form that sends no key must not throw away the provider's other keys.
func TestKeepKeysWhenFormSendsNoKey(t *testing.T) {
	old := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
		Key: testKeyA, KeyName: "个人",
		Keys: []ExtraKey{{Name: "团队", Key: testKeyB}}}
	p := keepKeys(old, Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1"})
	list := p.KeyList()
	if len(list) != 2 {
		t.Fatalf("keys dropped: %+v", list)
	}
	if !list[0].Active || list[0].Name != "个人" || list[0].ID != KeyRef(testKeyA) {
		t.Fatalf("front key changed: %+v", list[0])
	}
	if list[1].Name != "团队" || list[1].ID != KeyRef(testKeyB) || !list[1].On {
		t.Fatalf("extra key changed: %+v", list[1])
	}
}

// A form that saves an extra key's value as the front key keeps its name.
func TestKeepKeysKeepsNameOfExtraKeyPromotedByForm(t *testing.T) {
	old := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
		Key: testKeyA, KeyName: "个人",
		Keys: []ExtraKey{{Name: "团队", Key: testKeyB}}}
	p := keepKeys(old, Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: testKeyB})
	list := p.KeyList()
	if len(list) != 1 || list[0].Name != "团队" || !list[0].Active {
		t.Fatalf("name not kept: %+v", list)
	}
}

// Saving the front key again keeps its name, as before.
func TestKeepKeysKeepsFrontName(t *testing.T) {
	old := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1",
		Key: testKeyA, KeyName: "个人",
		Keys: []ExtraKey{{Name: "团队", Key: testKeyB}}}
	p := keepKeys(old, Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: testKeyA})
	list := p.KeyList()
	if len(list) != 2 || list[0].Name != "个人" || list[1].Name != "团队" {
		t.Fatalf("names changed: %+v", list)
	}
}
