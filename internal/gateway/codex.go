package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// The ChatGPT Codex backend only streams. A Responses request from a client
// is therefore always streamed upstream: a streaming client gets the events
// as they come, and a non-streaming client gets the finished response built
// from the same events.

// codexResponses serves a Responses request for the codex provider.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	var q struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &q)
	res, err := s.forward(r.Context(), p, provider.Responses, pathOf(provider.Responses), rewriteModel(body, model), r.Header)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	ct := res.Header.Get("Content-Type")
	if q.Stream {
		return relayCodexStream(w, res.Body, ct, u)
	}
	if !strings.HasPrefix(ct, "text/event-stream") {
		// not a stream: the reply is already a whole response
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		var v struct {
			Usage rUsage `json:"usage"`
		}
		if json.Unmarshal(b, &v) == nil {
			u.add(v.Usage.usage())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		w.Write(b)
		return res.StatusCode, ""
	}
	final, err := collectResponses(res.Body)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, 502, msg), msg
	}
	var v struct {
		Usage rUsage `json:"usage"`
	}
	if json.Unmarshal(final, &v) == nil {
		u.add(v.Usage.usage())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(final)
	return 200, ""
}

// relayCodexStream copies the event stream to the client as it comes, counting
// the usage on the way. A missing Content-Type is labelled as a stream.
func relayCodexStream(w http.ResponseWriter, src io.Reader, ct string, u *Usage) (int, string) {
	if ct == "" {
		ct = "application/json"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	if strings.HasPrefix(ct, "text/event-stream") {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(provider.Responses, ct)
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return 200, ""
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return 200, ""
}

// collectResponses reads a Codex event stream to its end and returns the
// finished response as compact JSON. The response is the one the stream
// closes with; when that is missing, the output items are assembled from the
// items the stream finished.
func collectResponses(src io.Reader) ([]byte, error) {
	items := map[int]json.RawMessage{}
	var final map[string]json.RawMessage
	var failure string
	readSSE(src, func(_, data string) error {
		var e struct {
			Type        string          `json:"type"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Response    json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(data), &e) != nil {
			return nil
		}
		switch e.Type {
		case "response.output_item.done":
			items[e.OutputIndex] = e.Item
		case "response.completed", "response.incomplete":
			var r map[string]json.RawMessage
			if json.Unmarshal(e.Response, &r) == nil {
				final = r
			}
		case "response.failed", "error":
			failure = provider.APIError([]byte(data), "the stream failed")
		}
		return nil
	})
	if final == nil {
		if failure != "" {
			return nil, errors.New(failure)
		}
		if len(items) == 0 {
			return nil, errors.New("the stream ended without a response")
		}
		final = map[string]json.RawMessage{"object": json.RawMessage(`"response"`), "status": json.RawMessage(`"completed"`)}
	}
	if out := final["output"]; len(out) == 0 || string(out) == "null" || string(out) == "[]" {
		idx := make([]int, 0, len(items))
		for i := range items {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		list := make([]json.RawMessage, 0, len(idx))
		for _, i := range idx {
			list = append(list, items[i])
		}
		b, _ := json.Marshal(list)
		final["output"] = b
	}
	return json.Marshal(final)
}

// labelEventStream gives a Codex reply that arrives without a Content-Type the
// type of a stream when its first bytes say so. Only a 200 reply is looked at.
func labelEventStream(res *http.Response) {
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "" {
		return
	}
	br := bufio.NewReader(res.Body)
	head, _ := br.Peek(6)
	head = bytes.TrimLeft(head, " \t\r\n")
	if bytes.HasPrefix(head, []byte("event:")) || bytes.HasPrefix(head, []byte("data:")) {
		res.Header.Set("Content-Type", "text/event-stream")
	}
	res.Body = struct {
		io.Reader
		io.Closer
	}{br, res.Body}
}
