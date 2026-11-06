package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// resolve finds the provider for a model reference. A Responses request may
// name a signed-in Codex login as "codex/<model>"; everything else goes
// through the configured providers as before.
func resolve(from provider.Protocol, id string) (provider.Provider, string, bool) {
	if from == provider.Responses {
		if p, model, ok := provider.AccountFor(id); ok && p.Account.Agent == "codex" {
			return p, model, true
		}
	}
	return provider.Resolve(id)
}

// codexResponses sends a Responses request to the ChatGPT backend that the
// Codex login pays for. The backend always streams, so a client that asked
// for a single JSON reply gets the final response object reassembled from
// the stream. clientModel is the model name as the client sent it.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, clientModel, model string, body []byte, u *Usage) (int, string) {
	var in struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &in)

	up := p.Prepare(rewriteModel(body, model))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, provider.CodexBase+"/responses", bytes.NewReader(up))
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "dial/"+Version)
	if err := p.Sign(r.Context(), req, provider.Responses, up); err != nil {
		msg := err.Error()
		return writeError(w, provider.Responses, 401, msg), msg
	}
	res, err := s.client.Do(req)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		msg := provider.ErrCodexSignedOut.Error()
		return writeError(w, provider.Responses, 401, msg), msg
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}

	br := bufio.NewReaderSize(res.Body, 32<<10)
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") && !(ct == "" && looksLikeEventStream(br)) {
		b, _ := io.ReadAll(io.LimitReader(br, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, provider.Responses, 502, msg), msg
	}

	if in.Stream {
		return s.codexStream(w, res, br, u)
	}
	return s.codexWhole(w, p, br, clientModel, u)
}

// codexStream relays the backend's event stream to the client unchanged,
// reading the usage on the way past.
func (s *Server) codexStream(w http.ResponseWriter, res *http.Response, br *bufio.Reader, u *Usage) (int, string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	for _, k := range []string{"Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(provider.Responses, "text/event-stream")
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
			if errors.Is(err, io.EOF) {
				return http.StatusOK, ""
			}
			return http.StatusOK, err.Error()
		}
	}
}

// codexWhole reads the whole event stream and answers with the final
// response object, as a non-streaming Responses call expects.
func (s *Server) codexWhole(w http.ResponseWriter, p provider.Provider, br *bufio.Reader, clientModel string, u *Usage) (int, string) {
	var raw bytes.Buffer
	var final json.RawMessage
	var failed string
	readSSE(io.TeeReader(br, &raw), func(event, data string) error {
		var v struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &v) != nil {
			return nil
		}
		typ := v.Type
		if typ == "" {
			typ = event
		}
		switch typ {
		case "response.completed", "response.incomplete":
			if len(v.Response) > 0 {
				final = v.Response
			}
		case "response.failed", "error":
			msg := v.Message
			if v.Error != nil && v.Error.Message != "" {
				msg = v.Error.Message
			}
			if msg == "" {
				msg = "the response failed"
			}
			failed = msg
		}
		return nil
	})
	sniff := newSniffer(provider.Responses, "text/event-stream")
	sniff.write(raw.Bytes())
	u.add(sniff.usage())

	if failed != "" {
		msg := p.Name + ": " + failed
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if final == nil {
		msg := p.Name + ": the reply ended without a response"
		return writeError(w, provider.Responses, 502, msg), msg
	}
	out := withFields(final, map[string]any{"object": "response", "model": clientModel})
	writeJSON(w, http.StatusOK, json.RawMessage(out))
	return http.StatusOK, ""
}

// looksLikeEventStream reports whether the reply starts like a server-sent
// event stream, for backends that send no Content-Type. It peeks only as far
// as needed, so a live stream is not held back.
func looksLikeEventStream(br *bufio.Reader) bool {
	const d, e = "data:", "event:"
	for n := 1; n <= 64; n++ {
		b, err := br.Peek(n)
		t := bytes.TrimLeft(b, " \t\r\n")
		if bytes.HasPrefix(t, []byte(d)) || bytes.HasPrefix(t, []byte(e)) {
			return true
		}
		if len(t) > 0 && !strings.HasPrefix(d, string(t)) && !strings.HasPrefix(e, string(t)) {
			return false
		}
		if err != nil {
			return false
		}
	}
	return false
}
