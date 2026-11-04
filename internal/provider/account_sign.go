package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	errCodexSignedOut   = errors.New("codex: not signed in to ChatGPT — run `codex login` again")
	errCopilotSignedOut = errors.New("copilot: not signed in to GitHub — sign in to Copilot again in your editor or CLI")
)

const (
	// sessionSlack is how long before expiry a Copilot session is exchanged again.
	sessionSlack = 5 * time.Minute
	// copilotIntegrationID identifies dial's requests to Copilot. It must be non-empty.
	copilotIntegrationID = "vscode-chat"
	copilotUserAgent     = "GitHubCopilotChat/0.26.7"
)

var copilotHTTP = &http.Client{Timeout: 30 * time.Second}

// Sign prepares req to be sent to this provider with the given protocol and
// body. A plain provider gets its key. An account provider gets the agent's
// token and the URL the backend expects. The body is not changed here, so
// call Prepare first.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req, proto, body)
	case "copilot":
		return signCopilot(ctx, req, proto, body)
	}
	return fmt.Errorf("%s is not a provider dial can sign requests for", p.Name)
}

// Prepare adjusts a request body to what the provider's backend requires. A
// plain provider's body is returned as it is.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.Account.Agent != "codex" {
		return body
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	// The ChatGPT backend only takes streamed, unstored requests, and its
	// input must be a list of items.
	m["stream"] = true
	m["store"] = false
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": s,
			}},
		}}
	}
	// Sampling and output-limit parameters the backend refuses. Reasoning
	// settings are kept.
	for _, k := range codexRefused {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

var codexRefused = []string{
	"max_output_tokens", "max_tokens", "max_completion_tokens",
	"temperature", "top_p", "presence_penalty", "frequency_penalty",
	"seed", "stop", "n",
}

func signCodex(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if proto != Responses {
		return fmt.Errorf("codex only takes Responses requests, not %s", proto)
	}
	// Refreshes the access token first when it is about to expire.
	c, err := codexUsable(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	setRequest(req, u, body)
	req.Header.Set("Authorization", "Bearer "+c.access)
	if c.accountID != "" {
		req.Header.Set("chatgpt-account-id", c.accountID)
	}
	return nil
}

func signCopilot(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if proto != Chat {
		return fmt.Errorf("copilot only takes Chat Completions requests, not %s", proto)
	}
	c, ok := loadCopilot()
	if !ok {
		return errCopilotSignedOut
	}
	s, err := copilotSessionFor(ctx, c.oauth)
	if err != nil {
		return err
	}
	u, err := url.Parse(s.api + "/chat/completions")
	if err != nil {
		return err
	}
	setRequest(req, u, body)
	setCopilotHeaders(req.Header, s.token)
	req.Header.Set("Content-Type", "application/json")
	initiator, vision := inspectChat(body)
	req.Header.Set("X-Initiator", initiator)
	if vision {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

// setRequest points req at u and gives it the body.
func setRequest(req *http.Request, u *url.URL, body []byte) {
	req.URL = u
	req.Host = u.Host
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.ContentLength = int64(len(body))
	}
}

// setCopilotHeaders are the identification headers Copilot expects.
func setCopilotHeaders(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
	h.Set("Editor-Version", "vscode/1.99.3")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	h.Set("User-Agent", copilotUserAgent)
	h.Set("Openai-Intent", "conversation-panel")
}

// inspectChat reports who started a Chat Completions request (the user or the
// agent, judged by the last message) and whether it carries an image.
func inspectChat(body []byte) (string, bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return "user", false
	}
	initiator := "agent"
	if v.Messages[len(v.Messages)-1].Role == "user" {
		initiator = "user"
	}
	vision := false
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, pt := range parts {
				if pt.Type == "image_url" {
					vision = true
				}
			}
		}
	}
	return initiator, vision
}

// ---- Copilot session tokens -------------------------------------------------

// copilotSession is a short-lived token from a GitHub OAuth token, with the
// API address it is good for.
type copilotSession struct {
	token   string
	api     string
	expires time.Time
}

var (
	sessionMu      sync.Mutex
	sessionByOAuth = map[string]copilotSession{}
)

// copilotSessionFor returns a cached session, exchanging again only when it is
// close to expiry. The lock also keeps concurrent callers from exchanging at once.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if s, ok := sessionByOAuth[oauth]; ok && time.Until(s.expires) > sessionSlack {
		return s, nil
	}
	s, err := exchangeCopilot(ctx, oauth)
	if err != nil {
		return copilotSession{}, err
	}
	sessionByOAuth[oauth] = s
	return s, nil
}

// copilotAPIBase is the cached Copilot API address, or the default when no
// session exists yet. It never touches the network.
func copilotAPIBase() string {
	c, ok := loadCopilot()
	if !ok {
		return defaultCopilotAPI
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if s, ok := sessionByOAuth[c.oauth]; ok && s.api != "" {
		return s.api
	}
	return defaultCopilotAPI
}

func exchangeCopilot(ctx context.Context, oauth string) (copilotSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return copilotSession{}, errCopilotSignedOut
		}
		return copilotSession{}, fmt.Errorf("copilot: token exchange: %s", APIError(b, res.Status))
	}
	var t struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &t) != nil || t.Token == "" || t.Endpoints.API == "" {
		return copilotSession{}, errors.New("copilot: unreadable token response")
	}
	expires := time.Now().Add(20 * time.Minute)
	if t.ExpiresAt > 0 {
		expires = time.Unix(t.ExpiresAt, 0)
	}
	return copilotSession{
		token:   t.Token,
		api:     strings.TrimRight(t.Endpoints.API, "/"),
		expires: expires,
	}, nil
}
