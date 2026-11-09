package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFix_LabelEventStreamKinds: a 200 reply without Content-Type is labelled
// as an event stream when it starts with any SSE field, comments included.
func TestFix_LabelEventStreamKinds(t *testing.T) {
	streams := []string{
		"event: response.created\ndata: {}\n\n",
		"data: {}\n\n",
		": keep-alive\n\n",
		"id: 7\ndata: {}\n\n",
		"retry: 500\n\n",
	}
	for _, body := range streams {
		res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(body))}
		labelEventStream(res)
		if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("body %q: Content-Type = %q, want text/event-stream", body, ct)
		}
		got, _ := io.ReadAll(res.Body)
		if string(got) != body {
			t.Errorf("body %q was altered to %q", body, got)
		}
	}
}

// TestFix_LabelEventStreamLeavesJSONAlone: a JSON reply gets no label.
func TestFix_LabelEventStreamLeavesJSONAlone(t *testing.T) {
	res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"output":[]}`))}
	labelEventStream(res)
	if ct := res.Header.Get("Content-Type"); ct != "" {
		t.Errorf("JSON reply labelled %q", ct)
	}
}
