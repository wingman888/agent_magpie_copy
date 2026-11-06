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

// codexResponses serves a Responses request for a Codex account. The ChatGPT
// backend only streams, so the upstream call always streams: a streaming
// client gets the events as they come, and a non-streaming client gets the
// finished response folded out of the stream.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
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
		return accountFailure(w, p, err)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		if res.StatusCode == http.StatusUnauthorized {
			msg = provider.CodexSignInExpired.Error()
		}
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}

	// The backend may leave out Content-Type. Its reply is then a stream when
	// it opens with a data: or event: line.
	br := bufio.NewReaderSize(res.Body, 32<<10)
	br.Peek(1)
	head, _ := br.Peek(br.Buffered())
	head = bytes.TrimLeft(head, " \t\r\n")
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") &&
		!bytes.HasPrefix(head, []byte("data:")) && !bytes.HasPrefix(head, []byte("event:")) {
		b, _ := io.ReadAll(io.LimitReader(br, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if in.Stream {
		return relayStream(w, br, u)
	}
	return foldStream(w, br, p, u)
}

// accountFailure answers a failure to sign a request for an account provider.
// A lost or expired sign-in is a 401 that tells the user to sign in again.
func accountFailure(w http.ResponseWriter, p provider.Provider, err error) (int, string) {
	if errors.Is(err, provider.ErrSignedOut) {
		return writeError(w, provider.Responses, 401, err.Error()), err.Error()
	}
	msg := p.Name + ": " + err.Error()
	return writeError(w, provider.Responses, 502, msg), msg
}

// relayStream copies the upstream event stream to the client as it arrives,
// and reads the usage on the way past.
func relayStream(w http.ResponseWriter, br io.Reader, u *Usage) (int, string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
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
			if err == io.EOF {
				return http.StatusOK, ""
			}
			return http.StatusOK, err.Error()
		}
	}
}

// foldStream reads the upstream stream to its end and returns the response
// object carried by response.completed, as one JSON reply.
func foldStream(w http.ResponseWriter, br io.Reader, p provider.Provider, u *Usage) (int, string) {
	var done json.RawMessage
	var failed string
	readSSE(br, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.completed", "response.incomplete":
			done = ev.Response
		case "response.failed", "error":
			failed = firstNonEmpty(ev.Message, responseErrorText(ev.Response), "the response failed")
		}
		return nil
	})
	if done == nil {
		msg := p.Name + ": " + firstNonEmpty(failed, "no response was completed")
		return writeError(w, provider.Responses, 502, msg), msg
	}
	var v struct {
		Usage rUsage `json:"usage"`
	}
	if json.Unmarshal(done, &v) == nil {
		u.add(v.Usage.usage())
	}
	out := []byte(done)
	dec := json.NewDecoder(bytes.NewReader(done))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) == nil && obj != nil {
		obj["object"] = "response"
		if b, err := json.Marshal(obj); err == nil {
			out = b
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, ""
}

// responseErrorText pulls error.message out of a response object.
func responseErrorText(raw json.RawMessage) string {
	var v struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &v) == nil && v.Error != nil {
		return v.Error.Message
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
