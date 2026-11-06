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
	p.Fetch(ctx)
	model := ""
	if ms := p.Exposed(); len(ms) > 0 {
		model = ms[0].ID
	} else if ms := p.Available(); len(ms) > 0 {
		model = ms[0].ID
	}
	var out []Result
	for _, proto := range p.Speaks() {
		var path, body string
		switch proto {
		case Chat:
			path = "/chat/completions"
			body = fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
		case Responses:
			path = "/responses"
			body = fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
		case Anthropic:
			path = "/v1/messages"
			body = fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
		}
		if p.Account != nil {
			// The login's own base and signature; the body is shaped the way
			// the backend requires, exactly as a real request would be.
			base, err := AccountBase(ctx, p)
			if err != nil {
				out = append(out, Result{Protocol: proto, Model: model, Error: err.Error()})
				continue
			}
			sent := p.Prepare([]byte(body))
			out = append(out, probe(ctx, proto, base+path, string(sent), model, func(req *http.Request) error {
				return p.Sign(ctx, req, proto, sent)
			}))
			continue
		}
		headers := map[string]string{"Authorization": "Bearer " + p.Key}
		if proto == Anthropic {
			headers = AuthHeaders(p, Anthropic)
		}
		sign := func(req *http.Request) error {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			return nil
		}
		out = append(out, probe(ctx, proto, p.Base(proto)+path, body, model, sign))
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

// probe posts body to url. sign attaches whatever the provider needs to the
// request before it is sent; it may also change the URL for account logins.
func probe(ctx context.Context, proto Protocol, url, body, model string, sign func(*http.Request) error) Result {
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
	if err := sign(req); err != nil {
		r.Error = err.Error()
		return r
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
