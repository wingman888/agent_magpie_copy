package gateway

import (
	"bytes"
	"net/http"
)

// Recent-call bodies are diagnostics, not an unbounded traffic log. Keeping the
// first 256 KiB is enough to inspect ordinary prompts and responses while
// preventing images or long streams from multiplying into tens of MiB across
// 40 calls.
const callBodyLimit = 256 << 10

type capturedBody struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *capturedBody) add(p []byte) {
	if c.buf.Len() >= callBodyLimit {
		c.truncated = c.truncated || len(p) > 0
		return
	}
	n := min(len(p), callBodyLimit-c.buf.Len())
	_, _ = c.buf.Write(p[:n])
	if n < len(p) {
		c.truncated = true
	}
}

func (c *capturedBody) text() string { return c.buf.String() }

func captureRequestBody(p []byte) (string, bool) {
	var c capturedBody
	c.add(p)
	return c.text(), c.truncated
}

type captureResponseWriter struct {
	http.ResponseWriter
	body capturedBody
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.body.add(p)
	return w.ResponseWriter.Write(p)
}

func (w *captureResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
