package gateway

// The signed-in Codex account as a provider. Its backend speaks only the
// Responses API, and only as a stream, with a few fields it refuses. Every
// client protocol is turned into a Responses request here; a client that asked
// for a whole reply gets the stream folded back into one.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// codexDrop are the request fields the ChatGPT backend rejects.
var codexDrop = []string{"max_output_tokens", "temperature", "top_p"}

// codexBody builds the Responses body for the Codex backend: model without the
// codex/ prefix, stream on, store off, refused fields removed, and a string
// input turned into a list of items.
func codexBody(from provider.Protocol, body []byte, req *Request, model string) ([]byte, error) {
	raw := body
	if from != provider.Responses {
		up := *req // a copy: the caller still needs the client's own stream flag
		up.Stream = true
		raw = build(provider.Responses, &up, model, provider.HostOf(provider.CodexBase))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	m["model"] = model
	m["stream"] = true
	m["store"] = false
	for _, k := range codexDrop {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	return json.Marshal(m)
}

// codexIsStream reports whether the upstream reply is an event stream. The
// backend sometimes sends no Content-Type, so the body's first byte decides:
// JSON opens with { or [, an event stream with a field name such as event: or data:.
func codexIsStream(contentType string, br *bufio.Reader) bool {
	if strings.HasPrefix(contentType, "text/event-stream") {
		return true
	}
	if strings.HasPrefix(contentType, "application/json") {
		return false
	}
	for i := 0; ; i++ {
		b, _ := br.Peek(i + 1)
		if len(b) <= i {
			return false
		}
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return false
		}
		return true
	}
}

// codex serves a request for a codex/<model> id.
func (s *Server) codex(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	model = strings.TrimPrefix(model, "codex/")
	up, err := codexBody(from, body, req, model)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	token, account, err := provider.CodexAuth(r.Context())
	if err != nil {
		return writeError(w, from, 401, err.Error()), err.Error()
	}
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, provider.CodexBase+"/responses", bytes.NewReader(up))
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("User-Agent", "dial/"+Version)
	hreq.Header.Set("Authorization", "Bearer "+token)
	if account != "" {
		hreq.Header.Set("chatgpt-account-id", account)
	}
	res, err := s.client.Do(hreq)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	br := bufio.NewReaderSize(res.Body, 32<<10)
	if !codexIsStream(res.Header.Get("Content-Type"), br) {
		b, _ := io.ReadAll(io.LimitReader(br, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	if req.Stream && from == provider.Responses {
		// the client speaks the backend's own protocol: relay it as it comes
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		sniff := newSniffer(from, "text/event-stream")
		defer func() { u.add(sniff.usage()) }()
		buf := make([]byte, 32<<10)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				sniff.write(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					return http.StatusOK, ""
				}
				if f != nil {
					f.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return http.StatusOK, ""
	}
	dec := decoder(provider.Responses)
	if req.Stream {
		enc := encoder(from, newSSEWriter(w), req.Model)
		var failed string
		readSSE(br, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		return http.StatusOK, failed
	}
	// whole reply: fold the stream into one result and render it in the client's shape
	var col collector
	readSSE(br, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, req.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, col.err
}
