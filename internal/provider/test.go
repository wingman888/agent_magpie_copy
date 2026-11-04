package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Result is what a probe of one endpoint came back with.
type Result struct {
	Protocol Protocol `json:"protocol"`
	OK       bool     `json:"ok"`
	Status   int      `json:"status,omitempty"`
	Millis   int64    `json:"ms"`
	Model    string   `json:"model,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Test sends the smallest possible request to each endpoint the vendor
// serves, with the first exposed model, and reports what came back.
func (p Provider) Test(ctx context.Context) []Result {
	if p.Account != nil {
		return p.testAccount(ctx)
	}
	p.Fetch(ctx)
	model := ""
	if ms := p.Exposed(); len(ms) > 0 {
		model = ms[0].ID
	} else if ms := p.Available(); len(ms) > 0 {
		model = ms[0].ID
	}
	var out []Result
	for _, proto := range p.Speaks() {
		var url, body string
		headers := map[string]string{"Authorization": "Bearer " + p.Key}
		switch proto {
		case Chat:
			url = p.Chat + "/chat/completions"
			body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
		case Responses:
			url = p.Responses + "/responses"
			body = fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
		case Anthropic:
			url = p.Anthropic + "/v1/messages"
			body = fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
			headers = AuthHeaders(p, Anthropic)
		}
		out = append(out, probe(ctx, proto, url, body, model, headers))
	}
	return out
}

// AuthHeaders is how a request to the vendor proves who it is. Anthropic's
// own API wants x-api-key alone; compatible vendors take either, so both.
func AuthHeaders(p Provider, proto Protocol) map[string]string {
	if p.Key == "" {
		return map[string]string{}
	}
	if proto == Anthropic {
		if strings.HasSuffix(p.Host(), "anthropic.com") {
			return map[string]string{"x-api-key": p.Key}
		}
		return map[string]string{"x-api-key": p.Key, "Authorization": "Bearer " + p.Key}
	}
	return map[string]string{"Authorization": "Bearer " + p.Key}
}

// accountProtocol is the protocol a signed-in account speaks upstream: the
// ChatGPT backend takes Responses, Copilot's session API takes Chat.
func accountProtocol(agent string) Protocol {
	if agent == "copilot" {
		return Chat
	}
	return Responses
}

// testAccount probes a signed-in account the way its agent calls the backend:
// the body is shaped by Prepare and the request is signed by Sign. Nothing is
// sent with a key, since an account has none.
func (p Provider) testAccount(ctx context.Context) []Result {
	p.Fetch(ctx)
	model := ""
	if ms := p.Exposed(); len(ms) > 0 {
		model = ms[0].ID
	} else if ms := p.Available(); len(ms) > 0 {
		model = ms[0].ID
	}
	proto := accountProtocol(p.Account.Agent)
	var body string
	if proto == Chat {
		body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
	} else {
		body = fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
	}
	pb := p.Prepare([]byte(body))
	sign := func(req *http.Request) error { return p.Sign(ctx, req, proto, pb) }
	// the placeholder URL is replaced by Sign with the backend's own address
	return []Result{probeWith(ctx, proto, "https://account.invalid/", string(pb), model, nil, sign)}
}

func probe(ctx context.Context, proto Protocol, url, body, model string, headers map[string]string) Result {
	return probeWith(ctx, proto, url, body, model, headers, nil)
}

// probeWith is probe, with an optional hook that signs the request before it
// is sent.
func probeWith(ctx context.Context, proto Protocol, url, body, model string, headers map[string]string, sign func(*http.Request) error) Result {
	r := Result{Protocol: proto, Model: model}
	if model == "" {
		r.Error = "no model to try: expose one, or refresh the model list"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if sign != nil {
		if err := sign(req); err != nil {
			r.Error = err.Error()
			return r
		}
	}
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = strings.TrimPrefix(err.Error(), "Post \""+url+"\": ")
		return r
	}
	defer res.Body.Close()
	r.Status = res.StatusCode
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		r.OK = true
		return r
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	r.Error = APIError(b, res.Status)
	return r
}

// APIError pulls the human message out of an error body when there is one.
func APIError(b []byte, fallback string) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(b, &v) == nil {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return s
		}
		if v.Message != "" {
			return v.Message
		}
	}
	if s := strings.TrimSpace(string(b)); s != "" && len(s) < 200 && !strings.HasPrefix(s, "<") {
		return fallback + ": " + s
	}
	return fallback
}
