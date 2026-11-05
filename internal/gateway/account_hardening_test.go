package gateway

import (
	"strings"
	"testing"
)

func TestCollectResponseFallbacks(t *testing.T) {
	// no completed event: the last event carrying a response wins
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"r1\",\"output\":[]}}\n\n"
	got, err := collectResponse(strings.NewReader(stream))
	if err != nil || !strings.Contains(string(got), `"id":"r1"`) {
		t.Fatalf("fallback to last response event: %s, %v", got, err)
	}

	// no events at all, but a plain JSON body: returned as it is
	plain := "{\"id\":\"r2\",\"output\":[]}\n"
	got, err = collectResponse(strings.NewReader(plain))
	if err != nil || string(got) != `{"id":"r2","output":[]}` {
		t.Fatalf("plain JSON body: %s, %v", got, err)
	}

	// an event carrying only output is the fallback itself
	outOnly := "data: {\"type\":\"x\",\"output\":[{\"type\":\"message\"}]}\n\n"
	got, err = collectResponse(strings.NewReader(outOnly))
	if err != nil || !strings.Contains(string(got), `"output"`) {
		t.Fatalf("output fallback: %s, %v", got, err)
	}

	// nothing usable: an error, not a made-up body
	if _, err := collectResponse(strings.NewReader("data: not json\n\n")); err == nil {
		t.Fatal("unusable stream must fail")
	}
}
