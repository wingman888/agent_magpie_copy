package gateway

// Requests to signed-in agent accounts (codex, copilot). The provider package
// finds the login, signs the request and shapes the body; this file moves the
// bytes. Nothing here is shared with the ordinary provider paths, which keep
// their own functions.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// accountAgents are the agents whose logins dial can lend out as providers.
var accountAgents = map[string]bool{"codex": true, "copilot": true}

// accountPlaceholder is the URL a signed request starts with. provider.Sign
// points it at the backend the agent really uses.
const accountPlaceholder = "https://account.invalid/"

// accountProto is the protocol an agent account speaks upstream: the ChatGPT
// backend takes Responses, Copilot's session API takes Chat Completions.
func accountProto(agent string) provider.Protocol {
	if agent == "copilot" {
		return provider.Chat
	}
	return provider.Responses
}

// signedOutMsg is the error for an agent whose login is gone.
func signedOutMsg(agent string) string {
	if agent == "copilot" {
		return "copilot 已登出，请重新登录（在编辑器或 Copilot CLI 中登录 GitHub 后重试）"
	}
	return "codex 已登出，请重新登录（运行 codex login 后重试）"
}

// signedIn reports whether the agent's login is still on disk.
func signedIn(agent string) bool {
	for _, a := range provider.Accounts() {
		if a.ID == agent {
			return true
		}
	}
	return false
}

// account serves a request whose model belongs to an agent account.
func (s *Server) account(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	agent := p.Account.Agent
	if !signedIn(agent) {
		msg := signedOutMsg(agent)
		return writeError(w, from, 401, msg), msg
	}
	to := accountProto(agent)
	if from == to && isStreamRequest(body) {
		return s.relayAccount(w, r, p, to, model, body, u)
	}
	return s.translateAccount(w, r, p, from, to, model, body, u)
}

func isStreamRequest(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// forwardAccount sends a body to the agent's backend, shaped and signed by
// the provider. The backend is always streamed, so callers must ask for it.
func (s *Server) forwardAccount(ctx context.Context, p provider.Provider, to provider.Protocol, body []byte) (*http.Response, error) {
	body = p.Prepare(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountPlaceholder, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("User-Agent", "dial/"+Version)
	// Sign errors name the agent already ("codex: 已登出…"), so they pass unwrapped.
	if err := p.Sign(ctx, req, to, body); err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	return res, nil
}

// sniffStream reports whether a body without a Content-Type is an event
// stream: the ChatGPT backend sends none, but its body starts with data: or
// event:.
func sniffStream(br *bufio.Reader) bool {
	head, _ := br.Peek(32)
	head = bytes.TrimLeft(head, " \t\r\n")
	return bytes.HasPrefix(head, []byte("data:")) || bytes.HasPrefix(head, []byte("event:"))
}

// replyBody gives the reply body and its content type. A reply that names no
// type is an event stream when its body looks like one, else JSON.
func replyBody(res *http.Response) (*bufio.Reader, string) {
	br := bufio.NewReader(res.Body)
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		if sniffStream(br) {
			ct = "text/event-stream"
		} else {
			ct = "application/json"
		}
	}
	return br, ct
}

// relayAccount passes a streamed Responses or Chat reply to the client as it
// arrives, with the model name swapped and the upstream's usage read on the way.
func (s *Server) relayAccount(w http.ResponseWriter, r *http.Request, p provider.Provider, proto provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forwardAccount(r.Context(), p, proto, rewriteModel(body, model))
	if err != nil {
		return writeError(w, proto, 502, err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, proto, res.StatusCode, msg), msg
	}
	br, ct := replyBody(res)
	h := w.Header()
	h.Set("Content-Type", ct)
	for _, k := range []string{"Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if strings.HasPrefix(ct, "text/event-stream") {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, ct)
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, ""
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, ""
}

// translateAccount speaks the agent's protocol upstream and the client's
// protocol to the client. The backend is always streamed; the client gets
// the event stream or one reply, as it asked.
func (s *Server) translateAccount(w http.ResponseWriter, r *http.Request, p provider.Provider, from, to provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	stream := req.Stream
	req.Stream = true
	res, err := s.forwardAccount(r.Context(), p, to, build(to, req, model, ""))
	if err != nil {
		return writeError(w, from, 502, err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	br, ct := replyBody(res)
	if !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(br, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	dec := decoder(to)
	if stream {
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
		return 200, failed
	}
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
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err
}
